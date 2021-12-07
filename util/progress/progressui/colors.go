package progressui

import (
	"os"
	"strings"

	"github.com/morikuni/aec"
)

var termColorMap = map[string]aec.ANSI{
	"":        colorRun,
	"default": aec.DefaultF,

	"black":   aec.BlackF,
	"red":     aec.RedF,
	"green":   aec.GreenF,
	"yellow":  aec.YellowF,
	"blue":    aec.BlueF,
	"magenta": aec.MagentaF,
	"cyan":    aec.CyanF,
	"white":   aec.WhiteF,

	"light-black":   aec.LightBlackF,
	"light-red":     aec.LightRedF,
	"light-green":   aec.LightGreenF,
	"light-yellow":  aec.LightYellowF,
	"light-blue":    aec.LightBlueF,
	"light-magenta": aec.LightMagentaF,
	"light-cyan":    aec.LightCyanF,
	"light-white":   aec.LightWhiteF,
}

func getTermColor() aec.ANSI {
	envColorString := os.Getenv("BUILDKIT_TERM_COLOR")
	c, ok := termColorMap[strings.ToLower(envColorString)]
	if !ok {
		return colorRun
	}
	return c
}
