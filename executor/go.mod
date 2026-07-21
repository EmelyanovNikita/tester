module executor

go 1.22

require (
	github.com/jhump/protoreflect v1.15.6
	google.golang.org/grpc v1.64.1
	protoloader v0.0.0-00010101000000-000000000000
)

replace protoloader => ../protoloader
