package proto

import (
	"context"
	"encoding/json"

	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding"
)

// CodecName is the registered gRPC codec used for MonStack agent messages.
const CodecName = "monstack-json"

type jsonCodec struct{}

func (jsonCodec) Marshal(v any) ([]byte, error)   { return json.Marshal(v) }
func (jsonCodec) Unmarshal(d []byte, v any) error { return json.Unmarshal(d, v) }
func (jsonCodec) Name() string                    { return CodecName }

func init() {
	encoding.RegisterCodec(jsonCodec{})
}

// ForceCodecCallOption returns a gRPC CallOption selecting the MonStack JSON codec.
func ForceCodecCallOption() grpc.CallOption {
	return grpc.CallContentSubtype(CodecName)
}

// Wire messages matching api/proto/agent.proto.

type RegisterRequest struct {
	AgentID      string            `json:"agent_id,omitempty"`
	Hostname     string            `json:"hostname"`
	IPAddress    string            `json:"ip_address"`
	OS           string            `json:"os"`
	Arch         string            `json:"arch"`
	AgentVersion string            `json:"agent_version"`
	Collectors   []string          `json:"collectors"`
	Labels       map[string]string `json:"labels,omitempty"`
}

type RegisterResponse struct {
	AgentID  string `json:"agent_id"`
	Accepted bool   `json:"accepted"`
	Message  string `json:"message,omitempty"`
}

type HealthCheckRequest struct {
	AgentID string `json:"agent_id,omitempty"`
}

type HealthCheckResponse struct {
	Healthy       bool   `json:"healthy"`
	ServerVersion string `json:"server_version"`
}

type Metric struct {
	Name      string            `json:"name"`
	Value     float64           `json:"value"`
	Timestamp int64             `json:"timestamp"`
	Labels    map[string]string `json:"labels,omitempty"`
}

type MetricsBatch struct {
	Metrics []Metric `json:"metrics"`
}

type Heartbeat struct {
	Status        string  `json:"status"`
	UptimeSeconds float64 `json:"uptime_seconds"`
}

type AgentMessage struct {
	AgentID   string        `json:"agent_id"`
	Timestamp int64         `json:"timestamp"`
	Metrics   *MetricsBatch `json:"metrics,omitempty"`
	Heartbeat *Heartbeat    `json:"heartbeat,omitempty"`
}

type ServerMessage struct {
	Type    string            `json:"type"` // ack | command | config_update
	Command string            `json:"command,omitempty"`
	Params  map[string]string `json:"params,omitempty"`
	Message string            `json:"message,omitempty"`
}

// AgentServiceServer defines the gRPC server contract for MonStack agents.
type AgentServiceServer interface {
	Register(context.Context, *RegisterRequest) (*RegisterResponse, error)
	HealthCheck(context.Context, *HealthCheckRequest) (*HealthCheckResponse, error)
	Connect(AgentService_ConnectServer) error
}

// AgentService_ConnectServer represents the server side of the bi-directional Connect stream.
type AgentService_ConnectServer interface {
	Send(*ServerMessage) error
	Recv() (*AgentMessage, error)
	grpc.ServerStream
}

type agentServiceConnectServer struct {
	grpc.ServerStream
}

func (x *agentServiceConnectServer) Send(m *ServerMessage) error {
	return x.ServerStream.SendMsg(m)
}

func (x *agentServiceConnectServer) Recv() (*AgentMessage, error) {
	m := new(AgentMessage)
	if err := x.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

// RegisterAgentServiceServer registers an AgentServiceServer with a gRPC server.
func RegisterAgentServiceServer(s grpc.ServiceRegistrar, srv AgentServiceServer) {
	s.RegisterService(&AgentService_ServiceDesc, srv)
}

func registerHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(RegisterRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AgentServiceServer).Register(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/monstack.agent.v1.AgentService/Register",
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(AgentServiceServer).Register(ctx, req.(*RegisterRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func healthCheckHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(HealthCheckRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(AgentServiceServer).HealthCheck(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/monstack.agent.v1.AgentService/HealthCheck",
	}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(AgentServiceServer).HealthCheck(ctx, req.(*HealthCheckRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func connectHandler(srv any, stream grpc.ServerStream) error {
	return srv.(AgentServiceServer).Connect(&agentServiceConnectServer{stream})
}

// AgentService_ServiceDesc is the gRPC ServiceDesc for AgentService.
var AgentService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "monstack.agent.v1.AgentService",
	HandlerType: (*AgentServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Register",
			Handler:    registerHandler,
		},
		{
			MethodName: "HealthCheck",
			Handler:    healthCheckHandler,
		},
	},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "Connect",
			Handler:       connectHandler,
			ServerStreams: true,
			ClientStreams: true,
		},
	},
	Metadata: "api/proto/agent.proto",
}

// AgentServiceClient defines the gRPC client contract for MonStack agents.
type AgentServiceClient interface {
	Register(ctx context.Context, in *RegisterRequest, opts ...grpc.CallOption) (*RegisterResponse, error)
	HealthCheck(ctx context.Context, in *HealthCheckRequest, opts ...grpc.CallOption) (*HealthCheckResponse, error)
	Connect(ctx context.Context, opts ...grpc.CallOption) (AgentService_ConnectClient, error)
}

// AgentService_ConnectClient represents the client side of the bi-directional Connect stream.
type AgentService_ConnectClient interface {
	Send(*AgentMessage) error
	Recv() (*ServerMessage, error)
	grpc.ClientStream
}

type agentServiceClient struct {
	cc grpc.ClientConnInterface
}

// NewAgentServiceClient creates a new AgentServiceClient on the given gRPC connection.
func NewAgentServiceClient(cc grpc.ClientConnInterface) AgentServiceClient {
	return &agentServiceClient{cc: cc}
}

func (c *agentServiceClient) Register(ctx context.Context, in *RegisterRequest, opts ...grpc.CallOption) (*RegisterResponse, error) {
	out := new(RegisterResponse)
	opts = append([]grpc.CallOption{ForceCodecCallOption()}, opts...)
	if err := c.cc.Invoke(ctx, "/monstack.agent.v1.AgentService/Register", in, out, opts...); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *agentServiceClient) HealthCheck(ctx context.Context, in *HealthCheckRequest, opts ...grpc.CallOption) (*HealthCheckResponse, error) {
	out := new(HealthCheckResponse)
	opts = append([]grpc.CallOption{ForceCodecCallOption()}, opts...)
	if err := c.cc.Invoke(ctx, "/monstack.agent.v1.AgentService/HealthCheck", in, out, opts...); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *agentServiceClient) Connect(ctx context.Context, opts ...grpc.CallOption) (AgentService_ConnectClient, error) {
	opts = append([]grpc.CallOption{ForceCodecCallOption()}, opts...)
	stream, err := c.cc.NewStream(ctx, &AgentService_ServiceDesc.Streams[0], "/monstack.agent.v1.AgentService/Connect", opts...)
	if err != nil {
		return nil, err
	}
	return &agentServiceConnectClient{stream}, nil
}

type agentServiceConnectClient struct {
	grpc.ClientStream
}

func (x *agentServiceConnectClient) Send(m *AgentMessage) error {
	return x.ClientStream.SendMsg(m)
}

func (x *agentServiceConnectClient) Recv() (*ServerMessage, error) {
	m := new(ServerMessage)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}
