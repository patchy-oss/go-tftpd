package main

import "git.scarlet.house/oss/go-tftpd"

func main() {
	server, err := tftpd.NewTFTPServer("8000")
	if err != nil {
		panic(err)
	}
	defer server.Close()
	server.ListenAndServe()
}
