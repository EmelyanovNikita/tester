package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/desc/protoparse"
)

func main() {
	protoPath := "proto"

	log.Printf("Загрузка proto файлов из: %s", protoPath)

	// Находим все .proto файлы
	filesList, err := filepath.Glob(filepath.Join(protoPath, "*.proto"))
	if err != nil {
		log.Fatalf("Ошибка поиска файлов: %v", err)
	}

	if len(filesList) == 0 {
		log.Fatalf("Не найдено .proto файлов в %s", protoPath)
	}

	log.Printf("Найдено файлов: %d", len(filesList))
	for _, f := range filesList {
		log.Printf("  - %s", f)
	}

	// Извлекаем только имена файлов без пути
	var fileNames []string
	for _, f := range filesList {
		fileNames = append(fileNames, filepath.Base(f))
	}

	log.Printf("Имена файлов: %v", fileNames)

	// Создаём парсер с ImportPaths = proto
	parser := protoparse.Parser{
		ImportPaths: []string{protoPath},
	}

	// Парсим файлы по одному
	filesMap := make(map[string]*desc.FileDescriptor)
	
	for _, fileName := range fileNames {
		log.Printf("Парсинг: %s", fileName)
		
		files, err := parser.ParseFiles(fileName)
		if err != nil {
			log.Printf("  Ошибка: %v", err)
			continue
		}
		
		for _, fd := range files {
			if _, exists := filesMap[fd.GetName()]; !exists {
				filesMap[fd.GetName()] = fd
			}
		}
	}

	if len(filesMap) == 0 {
		log.Fatalf("Не удалось распарсить ни одного файла")
	}

	log.Printf("Успешно распарсено файлов: %d", len(filesMap))

	type FieldInfo struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}

	type MethodInfo struct {
		Service        string      `json:"service"`
		Method         string      `json:"method"`
		Request        string      `json:"request"`
		Response       string      `json:"response"`
		RequestFields  []FieldInfo `json:"request_fields"`
		ResponseFields []FieldInfo `json:"response_fields"`
	}

	var methods []MethodInfo

	for _, fd := range filesMap {
		log.Printf("Обработка файла: %s", fd.GetName())
		
		for _, svc := range fd.GetServices() {
			serviceName := svc.GetFullyQualifiedName()
			log.Printf("  Найден сервис: %s", serviceName)

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

				methods = append(methods, MethodInfo{
					Service:        serviceName,
					Method:         m.GetName(),
					Request:        reqType.GetFullyQualifiedName(),
					Response:       respType.GetFullyQualifiedName(),
					RequestFields:  reqFields,
					ResponseFields: respFields,
				})
			}
		}
	}

	fmt.Println("\n=== НАЙДЕННЫЕ МЕТОДЫ ===\n")

	if len(methods) == 0 {
		fmt.Println("Методы не найдены. Проверьте структуру proto файлов.")
	} else {
		for _, m := range methods {
			fmt.Printf("Сервис: %s\n", m.Service)
			fmt.Printf("Метод: %s\n", m.Method)
			fmt.Printf("Запрос: %s\n", m.Request)
			fmt.Printf("Ответ: %s\n", m.Response)

			if len(m.RequestFields) > 0 {
				fmt.Println("Поля запроса:")
				for _, f := range m.RequestFields {
					fmt.Printf("  - %s: %s\n", f.Name, f.Type)
				}
			}

			if len(m.ResponseFields) > 0 {
				fmt.Println("Поля ответа:")
				for _, f := range m.ResponseFields {
					fmt.Printf("  - %s: %s\n", f.Name, f.Type)
				}
			}
			fmt.Println("---")
		}
	}

	jsonData, _ := json.MarshalIndent(methods, "", "  ")
	os.WriteFile("methods.json", jsonData, 0644)

	fmt.Printf("\nВсего методов: %d\n", len(methods))
	fmt.Println("Сохранено в methods.json")
}
