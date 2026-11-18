// Command tool is a root outside the product; what only it reaches does not count.
package main

import "example.com/emitters/guard"

func main() { _ = guard.ToolOnly() }
