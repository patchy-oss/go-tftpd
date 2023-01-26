package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"strings"
	"syscall"
)

const (
	bodyMaxSize      = 2048
	defaultBlockSize = 512
	hdrsize          = 4
)

func main() {
	server, err := newTFTPServer("8000")
	if err != nil {
		panic(err)
	}
	defer server.close()
	server.listenAndServe()
}

type tftpServer struct {
	listener    net.PacketConn
	connections map[string]*client
}

func newTFTPServer(port string) (*tftpServer, error) {
	listener, err := net.ListenPacket("udp", fmt.Sprintf(":%v", port))
	if err != nil {
		return nil, err
	}

	return &tftpServer{
		listener:    listener,
		connections: make(map[string]*client),
	}, nil
}

func (tftp *tftpServer) close() {
	for _, v := range tftp.connections {
		v.file.Close()
	}
	tftp.listener.Close()
}

func (tftp *tftpServer) listenAndServe() {
	body := make([]byte, bodyMaxSize)
	for {
		numRead, addr, err := tftp.listener.ReadFrom(body)
		if err != nil {
			log.Printf("error while reading packet: '%v'\n", err)
			continue
		}

		tftp.handleConnection(addr, numRead, body)
	}
}

func (tftp *tftpServer) handleConnection(addr net.Addr, numRead int, body []byte) {
	cli, ok := tftp.connections[addr.String()]
	if !ok {
		cli = newClient(addr)
	}

	err := func() error {
		req, err := newRequest(numRead, body)
		if err != nil {
			return err
		}

		err = tftp.handleRequest(cli, req)
		if err != nil {
			return err
		}

		resp := newResponse(cli, req)
		err = tftp.handleResponse(cli, resp)
		if err != nil {
			return err
		}

		_, err = tftp.sendResponse(cli, resp)
		return err
	}()

	if err != nil {
		tftpErr, ok := err.(*tftpError)
		if !ok {
			log.Printf("Got unexpected error: %v\n", err)
			tftpErr = newTFTPError(ecNDEF, "Unexpected error.")
		}
		_, err = tftp.sendError(cli, tftpErr)
		if err != nil {
			panic(err)
		}
	}
}

func (tftp *tftpServer) handleRequest(cli *client, req *request) error {
	// check for filename (wrq)
	// check for disk space
	// no such user ?

	// checking for illegal operations
	if req.opcode < opRRQ || req.opcode > opERROR {
		return newTFTPError(ecILL)
	}

	// checking for unknown client
	_, clientExists := tftp.connections[cli.tid.String()]
	if !clientExists && req.opcode != opRRQ && req.opcode != opWRQ {
		return newTFTPError(ecUTID)
	}

	// TODO: handle this properly (probably will need to close 'connection')
	if req.opcode == opERROR {
		log.Printf("Got error from client: '%s' (%v)\n", req.errorMessage, req.number)
		return nil
	}

	if !clientExists {
		log.Printf("Got new client: %v\n", cli.tid.String())

		err := cli.prepareFromRequest(req)
		if err != nil {
			return err
		}
		tftp.connections[cli.tid.String()] = cli
	}

	// TODO: last data packet, close the client!
	if req.opcode == opDATA {
		_, err := io.Copy(cli.file, bytes.NewReader(req.body))
		if err != nil {
			if errors.Is(err, syscall.ENOSPC) {
				err = newTFTPError(ecDSK)
			}
			return err
		}
	}

	return nil
}

func (tftp *tftpServer) handleResponse(cli *client, resp *response) error {
	if resp.opcode == opDATA {
		n, err := cli.file.Read(resp.body)
		if err != nil && err != io.EOF {
			return err
		}
		if cli.bytesLeft <= 0 {
			log.Printf("Client '%v' has received a file.\n", cli.tid.String())
			cli.file.Close()
			delete(tftp.connections, cli.tid.String())
		}
		resp.body = resp.body[:n]
		cli.bytesLeft -= int64(n)
	}

	return nil
}

func (tftp *tftpServer) sendError(cli *client, err *tftpError) (int, error) {
	log.Println(err)
	return tftp.sendResponse(cli, &response{opERROR, uint16(err.code), toCString(err.message.Error())})
}

func (tftp *tftpServer) sendResponse(cli *client, resp *response) (int, error) {
	header := []byte{0x0, byte(resp.opcode), 0x0, 0x0}
	binary.BigEndian.PutUint16(header[2:], resp.number)
	return tftp.listener.WriteTo(append(header, resp.body...), cli.tid)
}

type client struct {
	tid       net.Addr
	file      *os.File
	blockSize int
	bytesLeft int64
}

func newClient(tid net.Addr) *client {
	return &client{
		tid: tid,
	}
}

func (cli *client) prepareFromRequest(req *request) error {
	var err error
	var f *os.File

	// TODO: clean path to filename
	if req.opcode == opRRQ {
		f, err = os.Open(req.filename)
	} else {
		if _, err := os.Stat(req.filename); !errors.Is(err, fs.ErrNotExist) {
			return newTFTPError(ecFEX)
		}
		f, err = os.Create(req.filename)
	}
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			err = newTFTPError(ecFNF)
		case errors.Is(err, fs.ErrPermission):
			err = newTFTPError(ecACV)
		case errors.Is(err, syscall.ENOSPC):
			err = newTFTPError(ecDSK)
		}
		return err
	}

	stat, err := f.Stat()
	if err != nil {
		return err
	}

	cli.file = f
	cli.bytesLeft = stat.Size()
	cli.blockSize = defaultBlockSize

	return nil
}

type request struct {
	numRead int
	body    []byte

	opcode operation
	// depend on opcode
	number       uint16
	filename     string
	mode         string
	errorMessage string
}

func newRequest(numRead int, body []byte) (*request, error) {
	var n int
	var err error
	req := &request{
		numRead: numRead,
		body:    body,
	}

	req.opcode, req.body = operation(req.body[1]), req.body[2:]
	switch req.opcode {
	case opRRQ, opWRQ:
		n, req.filename, err = readCString(req.body)
		if err != nil {
			return nil, err
		}
		req.body = req.body[n:]

		n, req.mode, err = readCString(req.body)
		if err != nil {
			return nil, err
		}
		if req.mode != "octet" {
			return nil, newTFTPError(ecNDEF, "Incorrect mode '%v'. This server supports only 'octet' mode.", req.mode)
		}

		req.body = req.body[n:]

	case opDATA, opACK:
		req.number, req.body = binary.BigEndian.Uint16(req.body[:2]), req.body[2:]
		req.body = req.body[:req.numRead-hdrsize]

	case opERROR:
		req.number, req.body = binary.BigEndian.Uint16(req.body[:2]), req.body[2:]
		n, req.errorMessage, err = readCString(req.body)
		if err != nil {
			return nil, err
		}
		req.body = req.body[n:]
	}

	return req, nil
}

type response struct {
	opcode operation
	number uint16
	body   []byte
}

func newResponse(cli *client, req *request) *response {
	resp := &response{}

	switch req.opcode {
	case opRRQ, opACK:
		resp.body = make([]byte, cli.blockSize)
		resp.opcode = opDATA
		resp.number = req.number + 1

	case opWRQ, opDATA:
		resp.opcode = opACK
		resp.number = req.number
	}

	return resp
}

type tftpError struct {
	code    errorCode
	message error
}

func newTFTPError(code errorCode, clientMessage ...string) *tftpError {
	if code > ecNOUS {
		code = ecNDEF
	}

	message := tftpErrors[code]
	if code == ecNDEF {
		message = errors.New(strings.Join(clientMessage, " "))
	}

	return &tftpError{
		code:    code,
		message: message,
	}
}

func (err *tftpError) Error() string {
	return fmt.Sprintf("TFTP Error (%v): %v", err.code, err.message)
}

type errorCode uint16

const (
	ecNDEF errorCode = iota
	ecFNF
	ecACV
	ecDSK
	ecILL
	ecUTID
	ecFEX
	ecNOUS
)

var tftpErrors = [...]error{
	ecNDEF: errors.New(""),
	ecFNF:  errors.New("File not found."),
	ecACV:  errors.New("Access violation."),
	ecDSK:  errors.New("Disk full or allocation exceeded."),
	ecILL:  errors.New("Illegal TFTP operation."),
	ecUTID: errors.New("Unknown transfer ID."),
	ecFEX:  errors.New("File already exists."),
	ecNOUS: errors.New("No such user."),
}

type operation byte

const (
	opUNK operation = iota
	opRRQ
	opWRQ
	opDATA
	opACK
	opERROR
)

func toCString(src string) []byte {
	return append([]byte(src), 0x0)
}

func readCString(src []byte) (int, string, error) {
	end := 0
	for ; end < len(src) && src[end] != 0; end++ {
	}

	if end >= len(src) {
		return 0, "", fmt.Errorf("Incorrect c string")
	}

	return end + 1, string(src[:end]), nil
}
