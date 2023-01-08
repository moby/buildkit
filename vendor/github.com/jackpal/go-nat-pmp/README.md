go-nat-pmp
==========

A Go language client for the NAT-PMP internet protocol for port mapping and discovering the external
IP address of a firewall.

NAT-PMP is supported by Apple brand routers and open source routers like Tomato and DD-WRT.

See https://tools.ietf.org/rfc/rfc6886.txt


[![Build Status](https://travis-ci.org/jackpal/go-nat-pmp.svg)](https://travis-ci.org/jackpal/go-nat-pmp)

Get the package
---------------

    # Get the go-nat-pmp package.
    go get -u github.com/jackpal/go-nat-pmp
 
Usage
-----

Get one more package, used by the example code:

    go get -u github.com/jackpal/gateway
 
 Create a directory:
 
    cd ~/go
    mkdir -p src/hello
    cd src/hello

Create a file hello.go with these contents:

    package main

    import (
        "fmt"

        "github.com/jackpal/gateway"
        natpmp "github.com/jackpal/go-nat-pmp"
    )

    func main() {
        gatewayIP, err := gateway.DiscoverGateway()
        if err != nil {
            return
        }

        client := natpmp.NewClient(gatewayIP)
        response, err := client.GetExternalAddress()
        if err != nil {
            return
        }
        fmt.Printf("External IP address: %v\n", response.ExternalIPAddress)
    }

Build the example

    go build
    ./hello

    External IP address: [www xxx yyy zzz]

Clients
-------

This library is used in the Taipei Torrent BitTorrent client http://github.com/jackpal/Taipei-Torrent

Complete documentation
----------------------

    http://godoc.org/github.com/jackpal/go-nat-pmp

License
-------

This project is licensed under the Apache License 2.0.
