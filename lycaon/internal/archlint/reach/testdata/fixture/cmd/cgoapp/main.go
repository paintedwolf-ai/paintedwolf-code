package main

// int answer(void) { return 42; }
import "C"

func main() { _ = C.answer() }
