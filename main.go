package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
)

var (
	listener    net.PacketConn
	connections map[string]*client
)

func main() {
	const bodyMaxSize = 2048
	var err error
	listener, err = net.ListenPacket("udp", ":8000")
	check(err)
	defer listener.Close()
	connections = make(map[string]*client)

	var req request
	var addr net.Addr
	req.body = make([]byte, bodyMaxSize)
	for {
		req.numRead, addr, err = listener.ReadFrom(req.body)
		if err != nil {
			log.Printf("main: error while reading packet: '%v'\n", err)
			continue
		}
		handleRequest(addr, req)
	}
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

type operation byte

const (
	opUNK operation = iota
	opRRQ
	opWRQ
	opDATA
	opACK
	opERROR
)

func handleRequest(addr net.Addr, req request) {
	var err error
	req.opcode, req.body = operation(req.body[1]), req.body[2:]

	// checking for illegal operations
	if req.opcode < opRRQ || req.opcode > opERROR {
		_, err = sendError(addr, ecILL)
		check(err)
	}

	// checking for unknown client
	_, clientExists := connections[addr.String()]
	if !clientExists && req.opcode != opRRQ && req.opcode != opWRQ {
		_, err = sendError(addr, ecUTID)
		check(err)
	}

	switch req.opcode {
	case opRRQ, opWRQ:
		var n int
		n, req.filename, err = readCString(req.body)
		check(err)
		req.body = req.body[n:]

		n, req.mode, err = readCString(req.body)
		check(err)
		if req.mode != "octet" {
			check(fmt.Errorf("Incorrect mode '%v'. This server only supports 'octet' mode.\n", req.mode))
		}

		req.body = req.body[n:]

	case opDATA, opACK:
		req.number, req.body = binary.BigEndian.Uint16(req.body[:2]), req.body[2:]

	case opERROR:
		req.number, req.body = binary.BigEndian.Uint16(req.body[:2]), req.body[2:]
		_, req.errorMessage, err = readCString(req.body)
		check(err)
		log.Printf("Got error from client: '%s' (%v)\n", req.errorMessage, req.number)

		return
	}

	if !clientExists {
		addClient(addr, &req)
		check(err)
	}

	// check for filename (rrq)
	// check for disk space
	// check for file exist (wrq)
	// access violation ?
	// no such user ?

	formResponse(addr, &req)
}

type client struct {
	file      *os.File
	blockSize int
	bytesLeft int64
}

func addClient(addr net.Addr, req *request) {
	var err error
	var f *os.File

	if req.opcode == opRRQ {
		f, err = os.Open(req.filename)
	} else {
		f, err = os.Create(req.filename)
	}
	check(err)

	stat, err := f.Stat()
	check(err)

	connections[addr.String()] = &client{
		file:      f,
		blockSize: 512,
		bytesLeft: stat.Size(),
	}
}

type response struct {
	opcode operation
	number uint16
	body   []byte
}

func formResponse(addr net.Addr, req *request) {
	const hdrsize = 4
	var resp response
	var err error

	c := connections[addr.String()]
	switch req.opcode {
	case opRRQ:
		resp.body = make([]byte, c.blockSize)
		resp.opcode = opDATA
		resp.number = 1
		_, err = c.file.Read(resp.body)
		check(err)

	case opWRQ:
		resp.opcode = opACK

	case opDATA:
		_, err = io.CopyN(c.file, bytes.NewReader(req.body), int64(req.numRead-hdrsize))
		check(err)

		resp.opcode = opACK
		resp.number = req.number

		if req.numRead < c.blockSize+hdrsize {
			c.file.Close()
			delete(connections, addr.String())
		}

	case opACK:
		resp.body = make([]byte, c.blockSize)
		resp.opcode = opDATA
		resp.number = req.number + 1
		n, err := c.file.Read(resp.body)
		if err != nil && err != io.EOF {
			check(err)
		}
		if c.bytesLeft <= 0 {
			c.file.Close()
			delete(connections, addr.String())
		}
		resp.body = resp.body[:n]
		c.bytesLeft -= int64(n)

	case opERROR:
		return
	}

	_, err = sendResponse(addr, &resp)
	check(err)
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

var errorMessages = map[errorCode]string{
	ecNDEF: "",
	ecFNF:  "File not found.",
	ecACV:  "Access violation.",
	ecDSK:  "Disk full or allocation exceeded.",
	ecILL:  "Illegal TFTP operation.",
	ecUTID: "Unknown transfer ID.",
	ecFEX:  "File already exists.",
	ecNOUS: "No such user.",
}

func sendError(addr net.Addr, err errorCode, optMessage ...string) (int, error) {
	message := errorMessages[err]
	if err == ecNDEF {
		message = strings.Join(optMessage, "\n")
	}
	return sendResponse(addr, &response{opERROR, uint16(err), toCString(message)})
}

func sendResponse(addr net.Addr, resp *response) (int, error) {
	header := []byte{0x0, byte(resp.opcode), 0x0, 0x0}
	binary.BigEndian.PutUint16(header[2:], resp.number)
	return listener.WriteTo(append(header, resp.body...), addr)
}

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

func check(err error) {
	if err != nil {
		log.Fatalf("%v: %v\n", os.Args[0], err)
	}
}
