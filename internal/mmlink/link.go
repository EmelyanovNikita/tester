// Package mmlink — всё, что относится к соединению тестера с media-manager (MM).
//
// Как устроено соединение: тестер играет роль SSW. MM сам вызывает на тестере метод Connect и сообщает
// session_id и адрес, по которому тестеру нужно подключиться к MM. Дальше тестер подключается к этому адресу
// и аутентифицируется (CredentialsSend). Если порядок или содержание этих шагов поменяется,
// менять нужно только этот пакет: connect.go — сами шаги, link.go — ожидание Connect и «подключиться один раз».
package mmlink

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/grpc"
)

// Params — то, что MM сообщает в запросе Connect
type Params struct {
	SessionID string
	Address   string
	Port      int
}

// Session — установленное соединение с MM
type Session struct {
	ID   string
	Conn *grpc.ClientConn
}

// EstablishFunc устанавливает соединение с MM по параметрам из Connect: подключается и аутентифицируется.
// Именно эту функцию нужно заменить, если логика подключения изменится.
type EstablishFunc func(ctx context.Context, params Params) (*Session, error)

// Link хранит параметры из Connect и соединение с MM.
// Первый Connect от MM запоминается, повторные игнорируются; соединение устанавливается ровно один раз.
type Link struct {
	establish EstablishFunc

	paramsMu sync.Mutex
	params   Params
	ready    chan struct{} // закрывается после первого Connect

	ensureMu sync.Mutex // на время установки соединения: параллельные вызовы Ensure ждут друг друга
	sessMu   sync.Mutex
	session  *Session
}

func New(establish EstablishFunc) *Link {
	return &Link{
		establish: establish,
		ready:     make(chan struct{}),
	}
}

// OnConnect вызывается обработчиком запроса Connect от MM.
// Возвращает true, если это первый Connect и параметры приняты; повторный Connect игнорируется.
func (link *Link) OnConnect(params Params) bool {
	link.paramsMu.Lock()
	defer link.paramsMu.Unlock()

	select {
	case <-link.ready:
		return false
	default:
	}

	link.params = params
	close(link.ready)

	return true
}

// Ensure возвращает соединение с MM. Если оно уже установлено — просто возвращает его.
// Если нет — ждёт Connect от MM (пока не отменён ctx), затем подключается и аутентифицируется.
// После ошибки установки следующий вызов Ensure повторит попытку с теми же параметрами.
func (link *Link) Ensure(ctx context.Context) (*Session, error) {
	link.ensureMu.Lock()
	defer link.ensureMu.Unlock()

	if session := link.Session(); session != nil {
		return session, nil
	}

	select {
	case <-link.ready:
	case <-ctx.Done():
		return nil, fmt.Errorf("ожидание Connect от MM: %w", ctx.Err())
	}

	link.paramsMu.Lock()
	params := link.params
	link.paramsMu.Unlock()

	session, err := link.establish(ctx, params)
	if err != nil {
		return nil, err
	}

	link.sessMu.Lock()
	link.session = session
	link.sessMu.Unlock()

	return session, nil
}

// Session возвращает установленное соединение или nil, если его ещё нет
func (link *Link) Session() *Session {
	link.sessMu.Lock()
	defer link.sessMu.Unlock()

	return link.session
}
