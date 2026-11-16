//go:build windows

package main

import "desktoptoolbox/internal/win"

func aiDesktop() string { return win.DesktopDir() }
