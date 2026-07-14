//go:build windows

package launcher

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32Console            = syscall.NewLazyDLL("kernel32.dll")
	getConsoleScreenBufferInfo = kernel32Console.NewProc("GetConsoleScreenBufferInfo")
	fillConsoleOutputCharacter = kernel32Console.NewProc("FillConsoleOutputCharacterW")
	fillConsoleOutputAttribute = kernel32Console.NewProc("FillConsoleOutputAttribute")
	setConsoleCursorPosition   = kernel32Console.NewProc("SetConsoleCursorPosition")
)

type consoleCoordinate struct {
	X int16
	Y int16
}

type consoleRectangle struct {
	Left   int16
	Top    int16
	Right  int16
	Bottom int16
}

type consoleScreenBufferInfo struct {
	Size              consoleCoordinate
	CursorPosition    consoleCoordinate
	Attributes        uint16
	Window            consoleRectangle
	MaximumWindowSize consoleCoordinate
}

func clearTerminalFile(file *os.File) error {
	handle := uintptr(file.Fd())
	var info consoleScreenBufferInfo
	result, _, callErr := getConsoleScreenBufferInfo.Call(handle, uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		return fmt.Errorf("GetConsoleScreenBufferInfo: %w", callErr)
	}

	cells := uint32(info.Size.X) * uint32(info.Size.Y)
	origin := consoleCoordinate{}
	var written uint32
	result, _, callErr = fillConsoleOutputCharacter.Call(
		handle,
		uintptr(' '),
		uintptr(cells),
		coordinateValue(origin),
		uintptr(unsafe.Pointer(&written)),
	)
	if result == 0 {
		return fmt.Errorf("FillConsoleOutputCharacterW: %w", callErr)
	}
	result, _, callErr = fillConsoleOutputAttribute.Call(
		handle,
		uintptr(info.Attributes),
		uintptr(cells),
		coordinateValue(origin),
		uintptr(unsafe.Pointer(&written)),
	)
	if result == 0 {
		return fmt.Errorf("FillConsoleOutputAttribute: %w", callErr)
	}
	result, _, callErr = setConsoleCursorPosition.Call(handle, coordinateValue(origin))
	if result == 0 {
		return fmt.Errorf("SetConsoleCursorPosition: %w", callErr)
	}
	return nil
}

func coordinateValue(coordinate consoleCoordinate) uintptr {
	return uintptr(uint32(uint16(coordinate.X)) | uint32(uint16(coordinate.Y))<<16)
}
