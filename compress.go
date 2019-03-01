package compress

import (
	"compress/gzip"
	"io"
	"io/ioutil"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/google/brotli/go/cbrotli"
)

type (
	Config struct {
		Types     []string
		MinLength int64
		BrQuality int
		BrLGWin   int
		GzipLevel int
	}
	compressWriter struct {
		gin.ResponseWriter
		writer   io.Writer
		request  *http.Request
		config   Config
		encoding string
		gzipPool *sync.Pool
	}
)

func Compress(config Config) gin.HandlerFunc {
	gzipPool := &sync.Pool{
		New: func() interface{} {
			writer, err := gzip.NewWriterLevel(ioutil.Discard, config.GzipLevel)
			if err != nil {
				panic(err)
			}
			return writer
		},
	}

	return func(ctx *gin.Context) {
		encoding := getEncoding(ctx.Request)
		vary := ctx.Writer.Header().Get("Vary")
		if vary == "" {
			vary = "Accept-Encoding"
		} else {
			vary += ", Accept-Encoding"
		}
		ctx.Header("Vary", vary)
		// 没有编码
		if encoding == "" {
			ctx.Next()
			return
		}

		writer := &compressWriter{
			ResponseWriter: ctx.Writer,
			writer:         ctx.Writer,
			request:        ctx.Request,
			config:         config,
			encoding:       encoding,
			gzipPool:       gzipPool,
		}
		ctx.Writer = writer
		defer writer.close()
		ctx.Next()
	}
}

func getEncoding(req *http.Request) (encoding string) {
	if req.Method == http.MethodOptions {
		return
	}
	if req.Proto == "HTTP/1.0" {
		return
	}
	if strings.Contains(req.Header.Get("Connection"), "Upgrade") {
		return
	}

	for _, val := range strings.Split(req.Header.Get("Accept-Encoding"), ",") {
		val = strings.TrimSpace(val)
		if val == "br" {
			encoding = val
			break
		}
		if val == "gzip" {
			encoding = val
		}
	}
	return
}

func (w *compressWriter) WriteString(data string) (int, error) {
	return w.writer.Write([]byte(data))
}

func (w *compressWriter) Write(data []byte) (int, error) {
	if !w.Written() {
		w.open(int64(len(data)))
	}
	return w.writer.Write(data)
}

func (w *compressWriter) WriteHeader(code int) {
	w.ResponseWriter.WriteHeader(code)
}

func (w *compressWriter) open(contentLength int64) {
	header := w.Header()

	// 长度过滤
	if contentLength == -1 {
		if val, ok := header["Content-Length"]; ok && len(val) != 0 {
			if val, err := strconv.ParseInt(val[0], 10, 64); err != nil {
				contentLength = val
			}
		}
	}

	if w.config.MinLength >= contentLength {
		return
	}

	// 内容类型过滤
	var contentType []string
	var ok bool
	if contentType, ok = header["Content-Type"]; !ok || len(contentType) == 0 {
		return
	}
	mediatype, _, _ := mime.ParseMediaType(contentType[0])
	var typeMatch bool
	for _, typ := range w.config.Types {
		if mediatype == typ {
			typeMatch = true
			break
		}
	}
	if !typeMatch {
		return
	}

	header.Del("Content-Length")
	header.Set("Content-Encoding", w.encoding)

	// head 方法 无内容
	if w.request.Method == http.MethodHead {
		return
	}

	switch w.encoding {
	case "br":
		writer := cbrotli.NewWriter(w.ResponseWriter, cbrotli.WriterOptions{
			Quality: w.config.BrQuality,
			LGWin:   w.config.BrLGWin,
		})
		w.writer = writer
	case "gzip":
		writer := w.gzipPool.Get().(*gzip.Writer)
		writer.Reset(w.ResponseWriter)
		w.writer = writer
	}
}

func (w *compressWriter) close() {
	switch w.writer.(type) {
	case *gzip.Writer:
		writer := w.writer.(*gzip.Writer)
		writer.Close()
		w.gzipPool.Put(writer)
	case *cbrotli.Writer:
		writer := w.writer.(*cbrotli.Writer)
		writer.Close()
	}
}
