package main

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
)

var errNoBody = errors.New("sentinel error value")

type failureToReadBody struct{}

func (failureToReadBody) Read([]byte) (int, error) { return 0, errNoBody }
func (failureToReadBody) Close() error             { return nil }

// drainBody reads all of b to memory and then returns two equivalent
// ReadClosers yielding the same bytes.
//
// It returns an error if the initial slurp of all bytes fails. It does not attempt
// to make the returned ReadClosers have identical error-matching behavior.
func drainBody(b io.ReadCloser) (r1, r2 io.ReadCloser, err error) {
	if b == nil || b == http.NoBody {
		// No copying needed. Preserve the magic sentinel meaning of NoBody.
		return http.NoBody, http.NoBody, nil
	}
	var buf bytes.Buffer
	if _, err = buf.ReadFrom(b); err != nil {
		return nil, b, err
	}
	if err = b.Close(); err != nil {
		return nil, b, err
	}
	return io.NopCloser(&buf), io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}

// privateDumpResponse is like DumpRequest but dumps a response.
func privateDumpResponse(resp *http.Response, body bool) ([]byte, error) {
	var b bytes.Buffer
	var err error
	save := resp.Body
	savecl := resp.ContentLength

	if !body {
		// For content length of zero. Make sure the body is an empty
		// reader, instead of returning error through failureToReadBody{}.
		if resp.ContentLength == 0 {
			resp.Body = io.NopCloser(strings.NewReader(""))
		} else {
			resp.Body = failureToReadBody{}
		}
	} else if resp.Body == nil {
		resp.Body = io.NopCloser(strings.NewReader(""))
	} else {
		save, resp.Body, err = drainBody(resp.Body)
		if err != nil {
			return nil, err
		}
	}

	if resp.Header.Get("Content-Encoding") == "br" {

		// 注册 brotli 解压
		br := brotli.NewReader(resp.Body)
		defer resp.Body.Close()

		// 读取解压后的响应 body
		body, _ := io.ReadAll(br)
		resp.Body = io.NopCloser(bytes.NewBuffer(body))
	} else if resp.Header.Get("Content-Encoding") == "gzip" {

		// 注册 gzip 解压
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			log.Printf("gzip failed %s", err.Error())
		} else {
			defer resp.Body.Close()
			// 读取解压后的响应 body
			body, _ := io.ReadAll(gr)
			resp.Body = io.NopCloser(bytes.NewBuffer(body))
		}
	}

	err = resp.Write(&b)
	if err == errNoBody {
		err = nil
	}
	resp.Body = save
	resp.ContentLength = savecl
	if err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func main() {
	targetUrl := "https://api.openai.com" // 目标域名和端口
	target, err := url.Parse(targetUrl)
	if err != nil {
		log.Fatal(err)
	}

	// 创建反向代理
	proxy := httputil.NewSingleHostReverseProxy(target)

	// 修改请求头，将Host设置为目标域名
	proxy.Director = func(req *http.Request) {
		req.Host = target.Host
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
	}

	// 打印HTTP请求和响应的日志
	proxy.ModifyResponse = func(resp *http.Response) error {
		resCt := resp.Header.Get("Content-Type")
		if resCt == "text/event-stream" && resp.StatusCode == http.StatusOK {
			return nil
		}

		// 打印HTTP响应的日志
		responseDump, err := privateDumpResponse(resp, true)
		if err != nil {
			log.Printf("Failed to dump response: %v\n", err)
		} else {
			log.Printf("Response: \n%s\n", string(responseDump))
		}

		return nil
	}

	// 设置日志前缀和输出位置
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	// 启动HTTP服务器
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		//// 打印HTTP请求日志
		//contenType := r.Header.Get("Content-Type")
		//if !strings.Contains(contenType, "charset") {
		//	contenType += "; charset=utf-8"
		//	r.Header.Set("Content-Type", contenType)
		//}
		//raw, err := io.ReadAll(r.Body)
		//if err != nil {
		//	log.Printf("read request failed %s", err.Error())
		//	return
		//}
		//unquote, err := strconv.Unquote(string(raw))
		//if err != nil {
		//	log.Printf("read request failed %s", err.Error())
		//	r.Body = io.NopCloser(bytes.NewBuffer(raw))
		//} else {
		//	r.Body = io.NopCloser(bytes.NewBufferString(unquote))
		//}
		requestDump, err := httputil.DumpRequest(r, true)
		if err != nil {
			log.Printf("Failed to dump request: \n%v\n", err)
		} else {
			log.Printf("%s Request: %s\n", time.Now().Format("2006-01-02 15:04:05"), string(requestDump))
		}
		// 反向代理转发
		proxy.ServeHTTP(w, r)
	})

	log.Printf("Starting server on port 9000...\n")
	if err := http.ListenAndServe(":9000", nil); err != nil {
		log.Fatal(err)
	}
}
