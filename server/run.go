package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/soheilhy/cmux"
	"github.com/velonetics/lura/v2/config"
	"github.com/velonetics/lura/v2/logging"
	"github.com/velonetics/lura/v2/proxy"
	router "github.com/velonetics/lura/v2/router/gin"
	grpcconfig "github.com/velonetics/velonetics-grpc/v2/config"
	"github.com/velonetics/velonetics-grpc/v2/catalog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// RunServer wraps the HTTP server to multiplex gRPC on the same port when grpc.server is configured.
func RunServer(
	logger logging.Logger,
	registry *catalog.Registry,
	serviceCfg *grpcconfig.ServiceConfig,
	pf proxy.Factory,
	next router.RunServerFunc,
) router.RunServerFunc {
	if serviceCfg == nil || serviceCfg.Server == nil || len(serviceCfg.Server.Services) == 0 {
		return next
	}
	return func(ctx context.Context, cfg config.ServiceConfig, handler http.Handler) error {
		addr := cfg.Address
		if addr == "" {
			addr = "0.0.0.0"
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", addr, cfg.Port))
		if err != nil {
			return err
		}

		grpcServer := grpc.NewServer()
		reflection.Register(grpcServer)
		if err := registerServices(grpcServer, registry, serviceCfg, pf, logger); err != nil {
			return err
		}

		mux := cmux.New(listener)
		grpcListener := mux.MatchWithWriters(cmux.HTTP2MatchHeaderFieldSendSettings("content-type", "application/grpc"))
		httpListener := mux.Match(cmux.Any())

		httpServer := &http.Server{Handler: handler}
		var wg sync.WaitGroup
		errCh := make(chan error, 3)

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := grpcServer.Serve(grpcListener); err != nil {
				errCh <- err
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := httpServer.Serve(httpListener); err != nil && err != http.ErrServerClosed {
				errCh <- err
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := mux.Serve(); err != nil {
				errCh <- err
			}
		}()

		go func() {
			<-ctx.Done()
			grpcServer.GracefulStop()
			_ = httpServer.Shutdown(context.Background())
			_ = listener.Close()
		}()

		logger.Info("[SERVICE: gRPC]", "serving gRPC on", fmt.Sprintf("%s:%d", addr, cfg.Port))
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case err := <-errCh:
			wg.Wait()
			return err
		}
	}
}

func registerServices(
	grpcServer *grpc.Server,
	registry *catalog.Registry,
	serviceCfg *grpcconfig.ServiceConfig,
	pf proxy.Factory,
	logger logging.Logger,
) error {
	for _, svc := range serviceCfg.Server.Services {
		desc, err := registry.LookupService(svc.Name)
		if err != nil {
			return err
		}
		methods := map[string]grpcconfig.MethodPublishConfig{}
		for _, m := range svc.Methods {
			methods[m.Name] = m
		}
		sd := &grpc.ServiceDesc{
			ServiceName: svc.Name,
			Methods:     []grpc.MethodDesc{},
			Streams:     []grpc.StreamDesc{},
		}
		for i := 0; i < desc.Methods().Len(); i++ {
			methodDesc := desc.Methods().Get(i)
			pub, ok := methods[string(methodDesc.Name())]
			if !ok {
				continue
			}
			methodName := string(methodDesc.Name())
			sd.Methods = append(sd.Methods, grpc.MethodDesc{
				MethodName: methodName,
				Handler:    makeUnaryHandler(registry, desc, methodDesc, pub, pf, logger),
			})
		}
		if len(sd.Methods) == 0 {
			continue
		}
		grpcServer.RegisterService(sd, nil)
	}
	return nil
}

func makeUnaryHandler(
	registry *catalog.Registry,
	svc protoreflect.ServiceDescriptor,
	methodDesc protoreflect.MethodDescriptor,
	pub grpcconfig.MethodPublishConfig,
	pf proxy.Factory,
	logger logging.Logger,
) func(interface{}, context.Context, func(interface{}) error, grpc.UnaryServerInterceptor) (interface{}, error) {
	return func(_ interface{}, ctx context.Context, dec func(interface{}) error, _ grpc.UnaryServerInterceptor) (interface{}, error) {
		in := dynamicpb.NewMessage(methodDesc.Input())
		if err := dec(in); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "decode request: %v", err)
		}

		req, err := buildProxyRequest(ctx, in, pub)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "build request: %v", err)
		}

		proxyPipe, err := buildMethodProxy(pub, pf, logger)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "proxy: %v", err)
		}
		resp, err := proxyPipe(ctx, req)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "backend: %v", err)
		}
		out := dynamicpb.NewMessage(methodDesc.Output())
		if err := fillResponse(out, resp, registry, pub); err != nil {
			return nil, status.Errorf(codes.Internal, "response: %v", err)
		}
		return out, nil
	}
}

func buildProxyRequest(ctx context.Context, in proto.Message, pub grpcconfig.MethodPublishConfig) (*proxy.Request, error) {
	raw, err := protojson.Marshal(in)
	if err != nil {
		return nil, err
	}
	params := map[string]string{}
	for field, placeholder := range pub.PayloadParams {
		if v := extractField(in, field); v != "" {
			params[placeholder] = v
		}
	}
	headers := map[string][]string{}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		for _, allowed := range pub.InputHeaders {
			if allowed == "*" {
				for k, vals := range md {
					headers[http.CanonicalHeaderKey(k)] = vals
				}
				break
			}
			if vals := md.Get(strings.ToLower(allowed)); len(vals) > 0 {
				headers[http.CanonicalHeaderKey(allowed)] = vals
			}
		}
	}
	return &proxy.Request{
		Method:  http.MethodPost,
		Body:    io.NopCloser(strings.NewReader(string(raw))),
		Params:  params,
		Headers: headers,
	}, nil
}

func extractField(msg proto.Message, dotPath string) string {
	parts := strings.Split(dotPath, ".")
	cur := msg.ProtoReflect()
	for i, part := range parts {
		fd := cur.Descriptor().Fields().ByName(protoreflect.Name(part))
		if fd == nil {
			return ""
		}
		if i == len(parts)-1 {
			return cur.Get(fd).String()
		}
		if fd.Kind() != protoreflect.MessageKind {
			return ""
		}
		cur = cur.Get(fd).Message()
	}
	return ""
}

func buildMethodProxy(pub grpcconfig.MethodPublishConfig, pf proxy.Factory, logger logging.Logger) (proxy.Proxy, error) {
	if len(pub.Backends) == 0 {
		return nil, fmt.Errorf("no backends configured")
	}
	endpoint := &config.EndpointConfig{
		Endpoint: fmt.Sprintf("/grpc/%s", pub.Name),
		Backend:  pub.Backends,
	}
	return pf.New(endpoint)
}

func fillResponse(out proto.Message, resp *proxy.Response, _ *catalog.Registry, _ grpcconfig.MethodPublishConfig) error {
	if resp == nil {
		return nil
	}
	data, err := io.ReadAll(resp.Io)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(data, out)
}
