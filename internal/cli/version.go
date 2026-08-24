package cli

import "fmt"

// cmdVersion imprime la versión instalada del agente.
func cmdVersion() int {
	fmt.Printf("printpilot-agent %s\n", installedVersion())
	return 0
}
