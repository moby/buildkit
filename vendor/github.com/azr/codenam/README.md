# Codenamize

CLI  usage :
```shellsession
> go install github.com/azr/codenam/cmd/codenamize
> codenamize xyz
left-classroom
```

Library usage :

```go
	type digest string
	c := codenam.Ize(digest("xyz"))
	fmt.Println(c)

	c = codenam.Ize("abc")
	fmt.Println(c)

	cs := codenam.IzeSlice([]digest{"xyz", "abc"})
	fmt.Println(cs)

	cs = codenam.IzeSlice([]string{"abc", "def"})
	fmt.Println(cs)
	// Output: left-classroom
	// testy-give
	// [left-classroom testy-give]
	// [testy-give subsequent-win]
```

## Inspiration ?

This is Go a port from https://github.com/jjmontesl/codenamize

## Why name the package codenam ?

Because this is just meant to export two funcs and I wanted things to be
short yet addressable, "codenam.Ize".
