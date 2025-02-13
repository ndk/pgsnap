package pgsnap

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/jackc/pgmock"
	"github.com/jackc/pgproto3/v2"
)

var (
	EmptyScript = errors.New("script is empty")
)

func (s *Snap) getScript() (*pgmock.Script, error) {
	f, err := s.getFile()
	if err != nil {
		return nil, err
	}

	script, err := s.readScript(f)
	if err != nil {
		return nil, err
	}
	if len(script.Steps) < len(pgmock.AcceptUnauthenticatedConnRequestSteps())+1 {
		return script, EmptyScript
	}

	return script, nil
}

func (s *Snap) runFakePostgre(script *pgmock.Script) {
	go s.acceptConnForScrpt(script)
}

func (s *Snap) acceptConnForScrpt(script *pgmock.Script) {
	conn, err := s.l.Accept()
	if err != nil {
		s.errchan <- err
		return
	}
	defer conn.Close()

	err = conn.SetDeadline(time.Now().Add(time.Second))
	if err != nil {
		s.errchan <- err
		return
	}

	be := pgproto3.NewBackend(pgproto3.NewChunkReader(conn), conn)

	err = script.Run(be)
	if err != nil {
		s.waitTilSync(be)

		s.sendError(be, err)

		be.Send(&pgproto3.ErrorResponse{
			Severity:            "ERROR",
			SeverityUnlocalized: "ERROR",
			Message:             err.Error(),
		})
		be.Send(&pgproto3.ReadyForQuery{'I'})

		conn.(*net.TCPConn).SetLinger(0)
		s.errchan <- err
		return
	}

	s.done <- struct{}{}
}

func (s *Snap) waitTilSync(be *pgproto3.Backend) {
	for i := 0; i < 10; i++ {
		msg, err := be.Receive()
		if err != nil {
			continue
		}

		_, ok := msg.(*pgproto3.Sync)
		if ok {
			break
		}
	}
}

func (s *Snap) sendError(be *pgproto3.Backend, err error) {
	be.Send(&pgproto3.ErrorResponse{
		Severity:            "ERROR",
		SeverityUnlocalized: "ERROR",
		Message:             "pgsnap: diff:\n" + err.Error(),
	})
	be.Send(&pgproto3.ReadyForQuery{'I'})
}

func (s *Snap) readScript(f io.Reader) (*pgmock.Script, error) {
	script := &pgmock.Script{
		Steps: pgmock.AcceptUnauthenticatedConnRequestSteps(),
	}

	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		b := scanner.Bytes()

		if len(b) < 2 {
			continue
		}

		switch b[0] {
		case 'B':
			msg, err := s.unmarshalB(b[1:])
			if err != nil {
				return nil, err
			}
			script.Steps = append(script.Steps, pgmock.SendMessage(msg))
		case 'F':
			msg, err := s.unmarshalF(b[1:])
			if err != nil {
				return nil, err
			}
			script.Steps = append(script.Steps, pgmock.ExpectMessage(msg))
		}
	}

	return script, nil
}

func (s *Snap) unmarshalB(src []byte) (pgproto3.BackendMessage, error) {
	t := struct {
		Type string
	}{}

	json.Unmarshal(src, &t)

	var o pgproto3.BackendMessage

	switch t.Type {
	case "AuthenticationOK":
		o = &pgproto3.AuthenticationOk{}
	case "BackendKeyData":
		o = &pgproto3.BackendKeyData{}
	case "ParseComplete":
		o = &pgproto3.ParseComplete{}
	case "ParameterDescription":
		o = &pgproto3.ParameterDescription{}
	case "RowDescription":
		o = &pgproto3.RowDescription{}
	case "ReadyForQuery":
		o = &pgproto3.ReadyForQuery{}
	case "BindComplete":
		o = &pgproto3.BindComplete{}
	case "DataRow":
		o = &pgproto3.DataRow{}
	case "CommandComplete":
		o = &pgproto3.CommandComplete{}
	case "EmptyQueryResponse":
		o = &pgproto3.EmptyQueryResponse{}
	case "NoData":
		o = &pgproto3.NoData{}
	case "ErrorResponse":
		o = &pgproto3.ErrorResponse{}
	default:
		return nil, fmt.Errorf("B: unknown type `%s`", t.Type)
	}

	if err := json.Unmarshal(src, o); err != nil {
		return nil, err
	}

	return o, nil
}

func (s *Snap) unmarshalF(src []byte) (pgproto3.FrontendMessage, error) {
	t := struct {
		Type string
	}{}

	json.Unmarshal(src, &t)

	var o pgproto3.FrontendMessage

	switch t.Type {
	case "StartupMessage":
		o = &pgproto3.StartupMessage{}
	case "Parse":
		o = &pgproto3.Parse{}
	case "Query":
		o = &pgproto3.Query{}
	case "Describe":
		o = &pgproto3.Describe{}
	case "Sync":
		o = &pgproto3.Sync{}
	case "Bind":
		o = &pgproto3.Bind{}
	case "Execute":
		o = &pgproto3.Execute{}
	case "Terminate":
		o = &pgproto3.Terminate{}
	default:
		return nil, fmt.Errorf("F: unknown type `%s`", t.Type)
	}

	_ = json.Unmarshal(src, o)

	return o, nil
}
