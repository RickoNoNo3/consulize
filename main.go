package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
	"github.com/valyala/fasthttp"
	proxy "github.com/yeqown/fasthttp-reverse-proxy/v2"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	EnvTarget                         = os.Getenv("TARGET")
	EnvTransmitHealth                 = os.Getenv("TRANSMIT_HEALTH")
	EnvHealthPath                     = os.Getenv("HEALTH_PATH")
	EnvHealthTimeout                  = os.Getenv("HEALTH_TIMEOUT")
	EnvHealthInterval                 = os.Getenv("HEALTH_INTERVAL")
	EnvDeregisterCriticalServiceAfter = os.Getenv("DEREGISTER_CRITICAL_SERVICE_AFTER")
	EnvServiceName                    = os.Getenv("SERVICE_NAME")
	EnvServiceId                      = os.Getenv("SERVICE_ID")
	EnvServiceHost                    = os.Getenv("SERVICE_HOST_FROM_CONSUL")
	EnvServicePort                    = os.Getenv("SERVICE_PORT")
	EnvTagsJsonStr                    = os.Getenv("TAGS")
	EnvTagsFile                       = os.Getenv("TAGS_FILE")
	Target                            *url.URL
	TransmitHealth                    bool
	ServicePort                       int
	Tags                              []string
)

var registered = false
var logger = hclog.New(&hclog.LoggerOptions{
	Name: "consulize",
})

// 还有其他环境变量如 CONSUL_HTTP_ADDR 等，见 consul.api.DefaultConfig 的源码
func init() {
	proxy.SetProduction()
	var err error
	if EnvTarget == "" {
		EnvTarget = "http://127.0.0.1:80"
	}
	if Target, err = url.Parse(EnvTarget); err != nil {
		panic("reverse proxy target invalid!")
	}
	if TransmitHealth, err = strconv.ParseBool(EnvTransmitHealth); err != nil {
		TransmitHealth = false
	}
	if EnvHealthPath == "" {
		EnvHealthPath = "/health"
	}
	if EnvHealthTimeout == "" {
		EnvHealthTimeout = "3s"
	}
	if EnvHealthInterval == "" {
		EnvHealthInterval = "5s"
	}
	if EnvDeregisterCriticalServiceAfter == "" {
		EnvDeregisterCriticalServiceAfter = "30s"
	}
	EnvHealthPath = strings.TrimLeft(EnvHealthPath, "/")
	if EnvServiceName == "" {
		EnvServiceName = "consulize"
	}
	if EnvServiceId == "" {
		rand.Seed(time.Now().Unix())
		EnvServiceId = fmt.Sprintf("%s-%d", EnvServiceName, rand.Int()%900000+100000)
	}
	if EnvServiceHost == "" {
		EnvServiceHost = "127.0.0.1"
	}
	if v64, err := strconv.ParseInt(EnvServicePort, 10, 32); err == nil {
		ServicePort = int(v64)
	} else {
		ServicePort = 8890
	}
	if EnvTagsJsonStr == "" {
		EnvTagsJsonStr = "[]"
	}
	if err = json.Unmarshal(bytes.NewBufferString(EnvTagsJsonStr).Bytes(), &Tags); err != nil {
		Tags = []string{}
	}
	if EnvTagsFile != "" {
		tags2 := []string{}
		if data, err := ioutil.ReadFile(EnvTagsFile); err == nil {
			if err := json.Unmarshal(data, &tags2); err == nil {
				Tags = append(Tags, tags2...)
			}
		}
	}
}

func registerService() (client *api.Client, err error) {
	registration := &api.AgentServiceRegistration{
		ID:      EnvServiceId,
		Name:    EnvServiceName,
		Address: EnvServiceHost,
		Port:    ServicePort,
		Tags:    Tags,
		Check: &api.AgentServiceCheck{
			HTTP:                           fmt.Sprintf("http://%s:%d/%s", EnvServiceHost, ServicePort, EnvHealthPath),
			Timeout:                        EnvHealthTimeout,
			Interval:                       EnvHealthInterval,
			DeregisterCriticalServiceAfter: EnvDeregisterCriticalServiceAfter,
		},
	}
	client, err = api.NewClient(api.DefaultConfig())
	if err != nil {
		return
	}
	err = client.Agent().ServiceRegister(registration)
	if err != nil {
		return
	}
	registered = true
	return
}

func cleanRedundantOldServices(client *api.Client) {
	if registered {
		if svcs, err := client.Agent().ServicesWithFilter(
			fmt.Sprintf("Address==\"%s\" and Port==%d and ID!=\"%s\"", EnvServiceHost, ServicePort, EnvServiceId),
		); err == nil && svcs != nil {
			for _, v := range svcs {
				_ = client.Agent().ServiceDeregister(v.ID)
			}
		}
	}
}

func deregisterService(client *api.Client) {
	if registered {
		if err := client.Agent().ServiceDeregister(EnvServiceId); err != nil {
			logger.Warn("Cannot deregister the service. Please wait for auto deregister: ", err)
		} else {
			registered = false
		}
	}
}

func main() {
	if len(os.Args) > 1 && strings.HasSuffix(os.Args[1], "version") {
		fmt.Println("Consulize version: v0.1.1")
		return
	}
	var err error
	var returnCode = 0
	defer func() {
		if returnCode != 0 {
			os.Exit(returnCode)
		}
	}()

	// 监听系统信号
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)

	// 检查目标URL协议，是HTTP还是WebSocket, 然后准备Server对象
	var proxyHandler fasthttp.RequestHandler
	if Target.Scheme == "http" || Target.Scheme == "https" {
		// 开启HTTP服务器
		// 这里采取路径分离策略，把配置中的Path部分单独拿出来，加入到每个反代请求的Path上作为前缀
		// 而反代服务器在启动的时候只有host:port，不包含path

		// http://hello.world:233/ok   =>   hello.world:233   用来注册反代服务器
		targetServer := Target.Hostname()
		if Target.Port() != "" {
			targetServer += ":" + Target.Port()
		}
		// http://hello.world:233/ok   =>   /ok            用来叠加到每一个请求前
		pathPrefix := Target.EscapedPath()

		// proxy server
		proxyServer := proxy.NewReverseProxy(targetServer)
		if pathPrefix != "/" {
			proxyHandler = func(ctx *fasthttp.RequestCtx) {
				// http://hello.world:233/ok
				// CONSULIZE_HOST/welcome             =>   /ok/welcome
				ctx.Request.SetRequestURI(pathPrefix + string(ctx.Request.RequestURI()))
				proxyServer.ServeHTTP(ctx)
			}
		} else {
			proxyHandler = proxyServer.ServeHTTP
		}
	} else if Target.Scheme == "ws" || Target.Scheme == "wss" {
		// 开启Websocket服务器
		if proxyServer, err := proxy.NewWSReverseProxyWith(
			// here uses scheme
			proxy.WithURL_OptionWS(Target.String()),
		); err == nil {
			proxyHandler = proxyServer.ServeHTTP
		} else {
			logger.Error("Cannot make websocket proxies with URL: ", Target.String())
			returnCode = 1
			return
		}
	} else {
		logger.Error("Cannot make proxies with URL: ", Target.String())
		returnCode = 1
		return
	}
	server := &fasthttp.Server{
		Handler: func(ctx *fasthttp.RequestCtx) {
			path := string(ctx.Path())
			if !TransmitHealth && path == "/"+EnvHealthPath { // embedded health check
				ctx.SetStatusCode(200)
			} else {
				logger.Info(path)
				proxyHandler(ctx)
			}
		},
		CloseOnShutdown: true,
	}

	// 开启服务器, 将serverErr作为服务器开启和关闭以及关闭时消息的变量，为nil意味着没关闭，为http.ErrServerClosed意味着正常关闭，其他情况为异常关闭
	logger.Info("HTTP serving")
	var serverErr error
	go func() {
		serverErr = server.ListenAndServe(fmt.Sprintf(":%d", ServicePort))
		if serverErr == nil {
			serverErr = http.ErrServerClosed
		}
		<-done
	}()
	time.Sleep(2 * time.Second) // 等2s，如果服务器进程初始化时发生错误，那后面就都不用进了
	if serverErr != nil && serverErr != http.ErrServerClosed {
		logger.Error("HTTP serving serverError: ", serverErr)
		returnCode = 1
		return
	}

	// 注册Consul服务
	logger.Info("Consul service registering")
	var client *api.Client
	if client, err = registerService(); err != nil {
		logger.Error("Failed to register Consul service", err.Error())
		returnCode = 1
		return
	}
	defer deregisterService(client)
	cleanRedundantOldServices(client)
	logger.Info("Consul service registered")

	// 等待服务器发来的结束信号，或者系统发来的结束信号
	<-done
	// 收到结束信号后解除Consul服务，然后如果服务器还没关闭(serverErr==nil)的话，等待1s关闭服务器
	logger.Info("Consul service deregistering")
	deregisterService(client)
	if serverErr == nil {
		logger.Info("HTTP server is ready to shutdown")
		time.Sleep(time.Second)
		if err = server.Shutdown(); err != nil {
			logger.Error("HTTP server shutdown error: ", err)
			returnCode = 2
			return
		} else {
			time.Sleep(time.Second)
			logger.Info("stopped")
		}
	} else {
		logger.Info("stopped")
	}
}
