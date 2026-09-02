package cli

import (
	"fmt"
	"strings"
)

// asciiLogoLines is the "Georgia11" figlet rendering of "synctrades",
// generated with `figlet -f Georgia11.flf synctrades` rather than hand-typed,
// so its alignment is verified rather than guessed. One line per slice
// element, using whichever Go string form needs no escaping for that line's
// content (raw strings can't contain a backtick; double-quoted strings need
// backticks unescaped but literal quotes escaped).
var asciiLogoLines = []string{
	`                                                                    ,,`,
	"                                        mm                        `7MM",
	`                                        MM                          MM`,
	",pP\"Ybd `7M'   `MF'`7MMpMMMb.  ,p6\"bo mmMMmm `7Mb,od8 ,6\"Yb.   ,M\"\"bMM  .gP\"Ya  ,pP\"Ybd",
	"8I   `\"   VA   ,V    MM    MM 6M'  OO   MM     MM' \"'8)   MM ,AP    MM ,M'   Yb 8I   `\"",
	"`YMMMa.    VA ,V     MM    MM 8M        MM     MM     ,pm9MM 8MI    MM 8M\"\"\"\"\"\" `YMMMa.",
	"L.   I8     VVV      MM    MM YM.    ,  MM     MM    8M   MM `Mb    MM YM.    , L.   I8",
	"M9mmmP'     ,V     .JMML  JMML.YMbmd'   `Mbmo.JMML.  `Moo9^Yo.`Wbmd\"MML.`Mbmmd' M9mmmP'",
	`           ,V`,
	`        OOb"`,
}

const bannerTagline = "Focus on trades - automate the rest"

// printBanner shows the wordmark and tagline before the root command's help.
func printBanner() {
	fmt.Println(strings.Join(asciiLogoLines, "\n"))
	fmt.Println()
	fmt.Println(bannerTagline)
	fmt.Println()
}
