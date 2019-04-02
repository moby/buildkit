#VT100

[![Build Status](https://travis-ci.org/jaguilar/vt100.svg?branch=master)](https://travis-ci.org/jaguilar/vt100)

[![GoDoc](https://godoc.org/github.com/jaguilar/vt100?status.svg)](https://godoc.org/github.com/jaguilar/vt100)

This is a vt100 screen reader. It seems to do a pretty
decent job of parsing the nethack input stream, which
is all I want it for anyway.

Here is a screenshot of the HTML-formatted screen data:

![](_readme/screencap.png)

The features we currently support:

* Cursor movement
* Erasing
* Many of the text properties -- underline, inverse, blink, etc.
* Sixteen colors
* Cursor saving and unsaving
* UTF-8

Not currently supported (and no plans to support):

* Scrolling
* Prompts
* Other cooked mode features

The API is not stable! This is a v0 package.

## Demo

Try running the demo! Install nethack:

    sudo apt-get install nethack

Get this code:

    go get github.com/jaguilar/vt100
    cd $GOPATH/src/githib.com/jaguilar/vt100

Run this code:

    go run demo/demo.go -port=8080 2>/tmp/error.txt

Play some nethack and check out the resulting VT100 terminal status:

    # From another terminal . . .
    xdg-open http://localhost:8080/debug/vt100

The demo probably assumes Linux (it uses pty-related syscalls). I'll happily
accept pull requests that replicate the pty-spawning functions on OSX and 
Windows.