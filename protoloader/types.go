package protoloader

import (
	"github.com/jhump/protoreflect/desc"
)

type FieldInfo struct {
	Name string
	Type string
}

type MethodInfo struct {
	Service    string
	Method     string
	MethodDesc *desc.MethodDescriptor
	Request    *desc.MessageDescriptor
	Response   *desc.MessageDescriptor
	ReqFields  []FieldInfo
	RespFields []FieldInfo
}

type ProtoData struct {
	Methods map[string]MethodInfo // ключ — имя метода
	Enums   map[string]int32      // ключ — имя enum
}
