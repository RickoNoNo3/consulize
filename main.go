package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gorilla/mux"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/http/httputil"
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
	EnvServiceNamespace               = os.Getenv("SERVICE_NAMESPACE")
	EnvServicePartition               = os.Getenv("SERVICE_PARTITION")
	EnvTagsJsonStr                    = os.Getenv("TAGS")
	EnvTagsFile                       = os.Getenv("TAGS_FILE")
	Target                            *url.URL
	TransmitHealth                    bool
	ServicePort                       int
	Tags                              []string
)

// 还有其他环境变量如 CONSUL_HTTP_ADDR 等，见 consul.api.DefaultConfig 的源码
func init() {
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
		EnvServiceId = fmt.Sprintf("%s-%d", EnvServiceName, rand.Int())
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

func main() {
	var err error
	var registered = false
	var logger = hclog.New(&hclog.LoggerOptions{
		Name: "consulize",
	})
	// 准备好服务的注册信息
	registration := &api.AgentServiceRegistration{
		ID:        EnvServiceId,
		Name:      EnvServiceName,
		Address:   EnvServiceHost,
		Port:      ServicePort,
		Tags:      Tags,
		Namespace: EnvServiceNamespace,
		Partition: EnvServicePartition,
		Check: &api.AgentServiceCheck{
			HTTP:                           fmt.Sprintf("http://%s:%d/%s", EnvServiceHost, ServicePort, EnvHealthPath),
			Timeout:                        EnvHealthTimeout,
			Interval:                       EnvHealthInterval,
			DeregisterCriticalServiceAfter: EnvDeregisterCriticalServiceAfter,
		},
		//Tags: []string{
		//	"urlprefix-/no-regret/api/mail/ strip=/no-regret/api",
		//	"urlprefix-/no-regret/api/user/getUserByUserHash strip=/no-regret/api",
		//	"urlprefix-/no-regret/api/user/feedback strip=/no-regret/api",
		//},
	}

	// 启动client，注册服务
	client, err := api.NewClient(api.DefaultConfig())
	if err != nil {
		logger.Error("Consul client startup failed: ", err.Error())
	}
	err = client.Agent().ServiceRegister(registration)
	defer func() {
		if registered {
			if err = client.Agent().ServiceDeregisterOpts(EnvServiceId, &api.QueryOptions{
				Namespace: EnvServiceNamespace,
				Partition: EnvServicePartition,
			}); err != nil {
				logger.Warn("Cannot deregister the service. Please wait for auto deregister: ", err)
			} else {
				registered = false
			}
		}
	}()
	if err != nil {
		logger.Error("Consul service register failed: ", err.Error())
	}
	registered = true

	// 准备反向代理和健康检查Handler
	router := mux.NewRouter()
	if !TransmitHealth { // Transmit 时直接走/ (透传), 不走这个
		router.HandleFunc("/"+EnvHealthPath, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	}
	router.HandleFunc("/*", func(w http.ResponseWriter, r *http.Request) {
		proxy := httputil.NewSingleHostReverseProxy(Target)
		logger.Info(r.URL.RequestURI())
		proxy.ServeHTTP(w, r)
	})
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", ServicePort),
		Handler: router,
	}
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL)
	// 开启服务器
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP serving error: ", err)
		}
	}()
	logger.Info("HTTP serving")

	// 收到结束信号  等5s后关闭服务器
	<-done
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() {
		cancel()
	}()
	logger.Info("HTTP server is ready to shutdown")
	if err = server.Shutdown(ctx); err != nil {
		logger.Error("HTTP server shutdown error: ", err)
	} else {
		logger.Info("stopped")
	}
}
