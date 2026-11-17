package store

// Platform is reached on Linux.
func Platform() { linuxOnly() }

func linuxOnly() {}

func unusedLinux() {}
