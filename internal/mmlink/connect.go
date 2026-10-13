package mmlink

import (
	"context"
	"fmt"
	"log"
	"time"

	"tester_mm/internal/protoloader"

	"github.com/golang/protobuf/jsonpb"
	"github.com/jhump/protoreflect/dynamic"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Метод, которым тестер представляется MM после подключения, и имя, под которым он представляется
const (
	credentialsMethod = "CredentialsSend"
	instanceName      = "tester"
)

// NewEstablisher возвращает стандартную логику подключения: подключиться по адресу из Connect,
// затем отправить CredentialsSend. Метод CredentialsSend берётся из загруженных proto-файлов.
func NewEstablisher(methods map[string]protoloader.MethodInfo) EstablishFunc {
	return func(ctx context.Context, params Params) (*Session, error) {
		conn, err := Dial(ctx, params.Address, params.Port)
		if err != nil {
			return nil, err
		}

		log.Printf("Подключение к MM установлено")

		if err := Authenticate(ctx, conn, methods, params.SessionID); err != nil {
			_ = conn.Close()

			return nil, err
		}

		log.Printf("Аутентификация успешна")

		return &Session{ID: params.SessionID, Conn: conn}, nil
	}
}

// Dial подключается к MM; при неудаче повторяет попытку раз в секунду, пока не отменён ctx
func Dial(ctx context.Context, address string, port int) (*grpc.ClientConn, error) {
	target := fmt.Sprintf("%s:%d", address, port)

	for {
		log.Printf("Попытка подключения к %s...", target)

		attemptCtx, cancel := context.WithTimeout(ctx, 1*time.Second)

		conn, err := grpc.DialContext(
			attemptCtx,
			target,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
		)

		cancel()

		if err == nil {
			log.Printf("Клиент успешно подключен к %s", target)
			return conn, nil
		}

		if ctx.Err() != nil {
			return nil, fmt.Errorf("подключение к %s прервано: %w", target, ctx.Err())
		}

		log.Printf("Ошибка подключения: %v", err)
	}
}

// Authenticate отправляет CredentialsSend с session_id из Connect
func Authenticate(ctx context.Context, conn *grpc.ClientConn, methods map[string]protoloader.MethodInfo, sessionID string) error {
	info, ok := methods[credentialsMethod]
	if !ok {
		return fmt.Errorf("метод %s не найден в загруженных proto-файлах (нужен mm_server_api.proto)", credentialsMethod)
	}

	fullMethod := fmt.Sprintf("/%s/%s", info.Service, info.Method)
	log.Printf("Отправка CredentialsSend: %s", fullMethod)

	reqMsg := dynamic.NewMessage(info.Request)

	if err := reqMsg.TrySetFieldByName("session_id", sessionID); err != nil {
		return fmt.Errorf("поле session_id в %s: %w", credentialsMethod, err)
	}

	if err := reqMsg.TrySetFieldByName("instance", instanceName); err != nil {
		return fmt.Errorf("поле instance в %s: %w", credentialsMethod, err)
	}

	reqJSON, _ := reqMsg.MarshalJSONPB(&jsonpb.Marshaler{OrigName: true})
	log.Printf("CredentialsSend запрос: %s", string(reqJSON))

	respMsg := dynamic.NewMessage(info.Response)

	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := conn.Invoke(callCtx, fullMethod, reqMsg, respMsg); err != nil {
		return fmt.Errorf("вызов %s: %w", credentialsMethod, err)
	}

	respJSON, _ := respMsg.MarshalJSONPB(&jsonpb.Marshaler{OrigName: true})
	log.Printf("CredentialsSend ответ: %s", string(respJSON))

	return nil
}
