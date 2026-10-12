package scenario

import (
	"fmt"
	"log"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// Управление ходом сценария: выполнение блоков, циклы for, условия if, пауза sleep и вывод log.
// Работа с gRPC (вызов методов MM) находится в executor.go.

// Execute выполняет команды сценария по порядку.
// По умолчанию останавливается на первой ошибке и возвращает её.
// При ContinueOnError ошибки только логируются, а в конце возвращается общая ошибка.
func (executor *Executor) Execute(scenario *Scenario) error {
	executor.executed = 0
	executor.failed = 0

	if err := executor.runBlock("", scenario.Commands); err != nil {
		return err
	}

	if executor.failed > 0 {
		return fmt.Errorf("команд с ошибками: %d из %d", executor.failed, executor.executed)
	}

	log.Printf("Сценарий выполнен, команд: %d", executor.executed)
	return nil
}

// runBlock выполняет список команд.
// prefix — номер объемлющей команды с точкой ("" на верхнем уровне, "2." в теле второй команды):
// из него получаются номера 1, 2, 2.1, 2.2 для логов и сообщений об ошибках.
func (executor *Executor) runBlock(prefix string, commands []Command) error {
	for i, cmd := range commands {
		label := fmt.Sprintf("%s%d", prefix, i+1)

		if cmd.IsFor {
			if err := executor.runFor(label, cmd); err != nil {
				return err
			}

			continue
		}

		if cmd.IsIf {
			if err := executor.runIf(label, cmd); err != nil {
				return err
			}

			continue
		}

		executor.executed++

		if err := executor.runCommand(label, cmd); err != nil {
			if stop := executor.failCommand(label, err); stop != nil {
				return stop
			}
		}
	}

	return nil
}

// failCommand учитывает ошибку команды label.
// Возвращает ошибку остановки сценария или nil, если включён ContinueOnError.
// Вызывающий сам увеличивает executed: у команд-блоков (for, if) оно считается только при ошибке.
func (executor *Executor) failCommand(label string, err error) error {
	executor.failed++
	log.Printf("[%s] ОШИБКА: %v", label, err)

	if !executor.ContinueOnError {
		return fmt.Errorf("сценарий остановлен на команде %s: %w", label, err)
	}

	return nil
}

// runFor выполняет цикл for: тело повторяется для каждого значения из диапазона, обе границы включительно.
// Переменная цикла доступна в теле как обычная переменная и после цикла сохраняет последнее значение.
func (executor *Executor) runFor(label string, cmd Command) error {
	from, to, err := executor.loopBounds(cmd)
	if err != nil {
		executor.executed++

		return executor.failCommand(label, err)
	}

	log.Printf("[%s] for %s in %d..%d", label, cmd.LoopVar, from, to)

	for i := from; i <= to; i++ {
		executor.exprParser.vars.Set(cmd.LoopVar, strconv.FormatInt(i, 10))
		log.Printf("[%s] %s = %d", label, cmd.LoopVar, i)

		if err := executor.runBlock(label+".", cmd.Body); err != nil {
			return err
		}
	}

	return nil
}

// loopBounds вычисляет границы диапазона цикла
func (executor *Executor) loopBounds(cmd Command) (int64, int64, error) {
	from, err := executor.resolveInt(cmd.LoopFrom)
	if err != nil {
		return 0, 0, fmt.Errorf("for %s: начало диапазона: %w", cmd.LoopVar, err)
	}

	to, err := executor.resolveInt(cmd.LoopTo)
	if err != nil {
		return 0, 0, fmt.Errorf("for %s: конец диапазона: %w", cmd.LoopVar, err)
	}

	return from, to, nil
}

// resolveInt вычисляет выражение (число или переменную) и требует целое число
func (executor *Executor) resolveInt(token string) (int64, error) {
	resolved, err := executor.exprParser.ResolveValue(token)
	if err != nil {
		return 0, err
	}

	n, err := strconv.ParseInt(resolved, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("ожидается целое число, получено %q", resolved)
	}

	return n, nil
}

// runIf выполняет условие: ветку then, если оно истинно, иначе ветку else (если она есть)
func (executor *Executor) runIf(label string, cmd Command) error {
	result, err := executor.evalCondition(label, cmd)
	if err != nil {
		executor.executed++

		return executor.failCommand(label, err)
	}

	branch := cmd.Body
	if !result {
		branch = cmd.ElseBody
	}

	return executor.runBlock(label+".", branch)
}

// evalCondition вычисляет условие и пишет в лог, с какими значениями оно сравнивалось
func (executor *Executor) evalCondition(label string, cmd Command) (bool, error) {
	left, err := executor.condOperand(cmd.CondLeft)
	if err != nil {
		return false, fmt.Errorf("if: левая часть: %w", err)
	}

	right, err := executor.condOperand(cmd.CondRight)
	if err != nil {
		return false, fmt.Errorf("if: правая часть: %w", err)
	}

	result, err := compareValues(left, cmd.CondOp, right)
	if err != nil {
		return false, fmt.Errorf("if: %w", err)
	}

	log.Printf("[%s] if %s %s %s (%q %s %q) → %t", label, cmd.CondLeft, cmd.CondOp, cmd.CondRight, left, cmd.CondOp, right, result)

	return result, nil
}

// condOperand вычисляет операнд условия. Имя enum приводится к числу, поэтому
// resp.replyCode (в JSON ответа это имя, например "RC_SUCCESS") сравнивается с RC_SUCCESS (число 0) как надо.
// Строки в кавычках остаются строками.
func (executor *Executor) condOperand(token string) (string, error) {
	value, err := executor.exprParser.ResolveValue(token)
	if err != nil {
		return "", err
	}

	if !strings.HasPrefix(strings.TrimSpace(token), `"`) {
		return executor.enumToNumber(value), nil
	}

	return value, nil
}

// enumToNumber заменяет имя enum его числом; всё остальное возвращает без изменений
func (executor *Executor) enumToNumber(value string) string {
	if number, ok := executor.exprParser.enums[value]; ok {
		return strconv.Itoa(int(number))
	}

	return value
}

// compareValues сравнивает два значения. Если оба — числа, сравнение числовое и точное
// (64-битные ID не теряют точность), иначе == и != сравнивают строки, а < > <= >= дают ошибку.
func compareValues(left, op, right string) (bool, error) {
	l, lok := parseNumber(left)
	r, rok := parseNumber(right)

	if lok && rok {
		cmp := l.Cmp(r)

		switch op {
		case "==":
			return cmp == 0, nil
		case "!=":
			return cmp != 0, nil
		case "<":
			return cmp < 0, nil
		case ">":
			return cmp > 0, nil
		case "<=":
			return cmp <= 0, nil
		case ">=":
			return cmp >= 0, nil
		}

		return false, fmt.Errorf("неизвестный оператор %q", op)
	}

	switch op {
	case "==":
		return left == right, nil
	case "!=":
		return left != right, nil
	}

	return false, fmt.Errorf("оператор %s применим только к числам, получено %q и %q", op, left, right)
}

// parseNumber разбирает десятичное число точно; inf, nan, шестнадцатеричные и прочие формы числом не считаются
func parseNumber(s string) (*big.Rat, bool) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, false
	}

	return new(big.Rat).SetString(s)
}

// runLog выводит в лог значения всех аргументов одной строкой
func (executor *Executor) runLog(label string, cmd Command) error {
	text, err := executor.formatLog(cmd)
	if err != nil {
		return err
	}

	log.Printf("[%s] log: %s", label, text)

	return nil
}

// formatLog вычисляет аргументы log и склеивает их через пробел
func (executor *Executor) formatLog(cmd Command) (string, error) {
	parts := make([]string, 0, len(cmd.LogArgs))

	for i, arg := range cmd.LogArgs {
		value, err := executor.exprParser.ResolveValue(arg)
		if err != nil {
			return "", fmt.Errorf("log: аргумент %d: %w", i+1, err)
		}

		parts = append(parts, value)
	}

	return strings.Join(parts, " "), nil
}

// maxSleepSeconds — верхняя граница паузы: больше суток в тесте почти наверняка опечатка
const maxSleepSeconds = 86400

// resolveSeconds вычисляет выражение (число или переменную) как число секунд от 0 до maxSleepSeconds
func (executor *Executor) resolveSeconds(token string) (float64, error) {
	resolved, err := executor.exprParser.ResolveValue(token)
	if err != nil {
		return 0, err
	}

	seconds, err := strconv.ParseFloat(resolved, 64)
	if err != nil || math.IsNaN(seconds) || seconds < 0 || seconds > maxSleepSeconds {
		return 0, fmt.Errorf("ожидаются секунды от 0 до %d, получено %q", maxSleepSeconds, resolved)
	}

	return seconds, nil
}

// runSleep выполняет паузу sleep(секунды); секунды могут быть дробными
func (executor *Executor) runSleep(label string, cmd Command) error {
	seconds, err := executor.resolveSeconds(cmd.SleepArg)
	if err != nil {
		return fmt.Errorf("sleep: %w", err)
	}

	log.Printf("[%s] sleep %s с", label, strconv.FormatFloat(seconds, 'f', -1, 64))
	time.Sleep(time.Duration(seconds * float64(time.Second)))

	return nil
}
