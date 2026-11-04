// Switch Translator — распаковка игр Nintendo Switch и перевод текста и звука.
package main

import (
	"os"

	"switch-translator/internal/gui"
)

func main() {
	game := ""
	if len(os.Args) > 1 {
		game = os.Args[1]
	}
	gui.Run(game)
}
