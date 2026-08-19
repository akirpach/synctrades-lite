package cli

import "fmt"

// asciiLogo is the "standard" figlet rendering of "synctrades", generated with
// `figlet -f standard synctrades` rather than hand-typed, so its alignment is
// verified rather than guessed.
const asciiLogo = `
                      _                 _
 ___ _   _ _ __   ___| |_ _ __ __ _  __| | ___  ___
/ __| | | | '_ \ / __| __| '__/ _` + "`" + ` |/ _` + "`" + ` |/ _ \/ __|
\__ \ |_| | | | | (__| |_| | | (_| | (_| |  __/\__ \
|___/\__, |_| |_|\___|\__|_|  \__,_|\__,_|\___||___/
     |___/
`

const bannerTagline = "your Schwab trades, in your own Google Sheet"

// printBanner shows the wordmark and tagline before the root command's help.
func printBanner() {
	fmt.Print(asciiLogo)
	fmt.Println(bannerTagline)
	fmt.Println()
}
