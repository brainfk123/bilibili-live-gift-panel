//go:build (!windows && !linux) || (linux && !amd64)

package main

func renameBundleDirectory(string, string) error { return errCommand }
