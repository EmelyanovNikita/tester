package protoloader

import (
	"fmt"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/desc/protoparse"
)

func LoadProto(protoPath string, fileNames ...string) (*ProtoData, error) {
	if len(fileNames) == 0 {
		return nil, fmt.Errorf("не переданы proto файлы")
	}

	parser := protoparse.Parser{
		ImportPaths: []string{protoPath},
	}

	files, err := parser.ParseFiles(fileNames...)
	if err != nil {
		return nil, fmt.Errorf("загрузка proto: %v", err)
	}

	data := &ProtoData{
		Methods: make(map[string]MethodInfo),
		Enums:   make(map[string]int32),
	}

	for _, fd := range files {
		for k, v := range extractMethods(fd) {
			data.Methods[k] = v
		}
		for k, v := range extractEnums(fd) {
			data.Enums[k] = v
		}
	}

	return data, nil
}

func extractMethods(fd *desc.FileDescriptor) map[string]MethodInfo {
	methods := make(map[string]MethodInfo)

	for _, svc := range fd.GetServices() {
		serviceName := svc.GetFullyQualifiedName()

		for _, m := range svc.GetMethods() {
			reqType := m.GetInputType()
			respType := m.GetOutputType()

			var reqFields []FieldInfo
			for _, f := range reqType.GetFields() {
				reqFields = append(reqFields, FieldInfo{
					Name: f.GetName(),
					Type: f.GetType().String(),
				})
			}

			var respFields []FieldInfo
			for _, f := range respType.GetFields() {
				respFields = append(respFields, FieldInfo{
					Name: f.GetName(),
					Type: f.GetType().String(),
				})
			}

			methods[m.GetName()] = MethodInfo{
				Service:    serviceName,
				Method:     m.GetName(),
				MethodDesc: m,
				Request:    reqType,
				Response:   respType,
				ReqFields:  reqFields,
				RespFields: respFields,
			}
		}
	}

	return methods
}

func extractEnums(fd *desc.FileDescriptor) map[string]int32 {
	enumMap := make(map[string]int32)

	for _, enum := range fd.GetEnumTypes() {
		values := enum.GetValues() // ← срез
		for _, value := range values {
			enumMap[string(value.GetName())] = int32(value.GetNumber())
		}
	}

	return enumMap
}
