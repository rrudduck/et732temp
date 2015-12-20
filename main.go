package main

import (
	"fmt"
	"github.com/kidoman/embd"
	_ "github.com/kidoman/embd/host/rpi"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	NumBytes   = 13
	NumNibbles = NumBytes * 2
	NumBits    = NumBytes * 8

	OneClockUs = 250
	TwoClockUs = OneClockUs * 2

	OneClockMinUs    = 0
	OneClockMaxUs    = 375
	TwoClockMinUs    = 400
	TwoClockMaxUs    = 750
	TwentyClockMinUs = 4500
	TwentyClockMaxUs = 5500

	IdleState     State = 0
	PreambleState State = 1
	DataState     State = 2

	ShortPulseWidth       PulseWidth = 0
	OneClockPulseWidth    PulseWidth = 1
	TwoClockPulseWidth    PulseWidth = 2
	TwentyClockPulseWidth PulseWidth = 3
	LongPulseWidth        PulseWidth = 4
)

type State int
type PulseWidth int

type Pulse struct {
	Width PulseWidth
	Edge  int
}

var CurrentState State = IdleState
var BitCount int = 0
var WaitCount int = 0

var StartTime time.Time = time.Now()
var CurrentPulse Pulse
var Data []int = make([]int, NumBits)

var DataPin embd.DigitalPin
var SyncPin embd.DigitalPin
var ErrorPin embd.DigitalPin

var Completed chan []int = make(chan []int)

func main() {
	embd.InitGPIO()
	defer embd.CloseGPIO()

	fmt.Println("Initializing pins.")

	var err error

	DataPin, err = embd.NewDigitalPin(12)
	if err != nil {
		fmt.Println("Could not initialize data pin.")
		return
	}

	SyncPin, err = embd.NewDigitalPin(16)
	if err != nil {
		fmt.Println("Could not initialize sync pin.")
		return
	}

	ErrorPin, err = embd.NewDigitalPin(20)
	if err != nil {
		fmt.Println("Could not initialize error pin.")
		return
	}

	rx, err := embd.NewDigitalPin(21)
	if err != nil {
		fmt.Println("Could not initialize rx pin.")
		return
	}

	fmt.Println("Setting pin directions.")

	if err = DataPin.SetDirection(embd.Out); err != nil {
		fmt.Printf("Could not set direction on data pin. The error was: %v\n", err)
		return
	}

	if err = SyncPin.SetDirection(embd.Out); err != nil {
		fmt.Printf("Could not set direction on sync pin. The error was: %v\n", err)
		return
	}

	if err = ErrorPin.SetDirection(embd.Out); err != nil {
		fmt.Printf("Could not set direction on error pin. The error was: %v\n", err)
		return
	}

	if err = rx.SetDirection(embd.In); err != nil {
		fmt.Printf("Could not set direction on rx pin. The error was: %v\n", err)
		return
	}

	FlashLed(DataPin)
	FlashLed(SyncPin)
	FlashLed(ErrorPin)

	fmt.Println("Setting watch on rx pin.")
	if err = rx.Watch(embd.EdgeBoth, InterruptHandler); err != nil {
		fmt.Printf("Could not set watch on rx pin. The error was: %v\n", err)
		return
	}

	quit := make(chan interface{})
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	signal.Notify(c, syscall.SIGTERM)
	go func() {
		<-c
		rx.StopWatching()
		close(quit)
	}()

	go func() {
		for m := range Completed {
			hex := NibbleToHex(m)
			fmt.Printf("Timestamp: %v\n", time.Now())
			fmt.Printf("Temp (Probe 1): %v\n", GetProbeTemp(1, hex))
			fmt.Printf("Temp (Probe 2): %v\n", GetProbeTemp(2, hex))
			fmt.Printf("Data: %v\n", hex)
		}
	}()

	<-quit
}

func InterruptHandler(pin embd.DigitalPin) {
	SyncPin.Write(embd.High)
	DataPin.Write(embd.Low)
	currentTime := time.Now()
	pulseTime := currentTime.Sub(StartTime).Nanoseconds() / 1000

	val, _ := pin.Read()
	if val == 1 {
		val = 0
	} else {
		val = 1
	}

	CurrentPulse = Pulse{
		Edge: val,
	}

	if pulseTime < OneClockMinUs {
		CurrentPulse.Width = ShortPulseWidth
	} else if pulseTime <= OneClockMaxUs {
		CurrentPulse.Width = OneClockPulseWidth
	} else if pulseTime <= TwoClockMaxUs {
		CurrentPulse.Width = TwoClockPulseWidth
	} else if pulseTime >= TwentyClockMinUs && pulseTime <= TwentyClockMaxUs {
		CurrentPulse.Width = TwentyClockPulseWidth
	} else {
		CurrentPulse.Width = LongPulseWidth
	}

	switch CurrentState {
	case IdleState:
		if CurrentPulse.Width == TwentyClockPulseWidth && CurrentPulse.Edge == 0 {
			BitCount = 0
			WaitCount = 0
			CurrentState = PreambleState
		}
		break
	case PreambleState:
		if CurrentPulse.Width == TwoClockPulseWidth {
			edge := CurrentPulse.Edge
			BitCount++
			Data[BitCount] = edge ^ 1
			BitCount++
			Data[BitCount] = edge
			CurrentState = DataState
		} else if CurrentPulse.Width == OneClockPulseWidth && CurrentPulse.Edge == 1 {
			// do nothing
		} else if CurrentPulse.Width == TwentyClockPulseWidth && CurrentPulse.Edge == 0 {
			// do nothing
		} else {
			CurrentState = IdleState
			fmt.Println("Error in preamble.")
			fmt.Printf("State: Pulse = %v, Width = %v, Edge = %v, WaitCount = %v, BitCount = %v\n", pulseTime, CurrentPulse.Width, CurrentPulse.Edge, WaitCount, BitCount )
		}
		break
	case DataState:
		DataPin.Write(embd.High)
		if CurrentPulse.Width == OneClockPulseWidth {
			if WaitCount == 0 {
				WaitCount++
			} else {
				Data[BitCount] = Data[BitCount-1]
				BitCount++
				WaitCount = 0
			}
		} else if CurrentPulse.Width == TwoClockPulseWidth {
			if WaitCount == 1 {
				CurrentState = IdleState
			} else {
				Data[BitCount] = Data[BitCount-1] ^ 1
				BitCount++
			}
		} else {
			FlashLed(ErrorPin)
			CurrentState = IdleState
			fmt.Println("Error in data.")
			fmt.Printf("State: Pulse = %v, Width = %v, Edge = %v, WaitCount = %v, BitCount = %v\n", pulseTime, CurrentPulse.Width, CurrentPulse.Edge, WaitCount, BitCount )
		}

		if BitCount >= NumBits {
			Completed <- Data
			Data = make([]int, NumBits)
			CurrentState = IdleState
		}
		break
	}

	StartTime = currentTime
	SyncPin.Write(embd.Low)
}

func FlashLed(pin embd.DigitalPin) {
	pin.Write(embd.High)
	time.Sleep(1 * time.Second)
	pin.Write(embd.Low)
}

func NibbleToHex(in []int) []int {
	out := make([]int, NumNibbles)
	for i := 0; i < NumNibbles; i++ {
		out[i] = 0
		for j := 0; j < 4; j++ {
			out[i] <<= 1
			temp := in[(i*4)+j]
			out[i] = out[i] | temp
		}
	}

	return out
}

func GetProbeTemp(probeId int, data []int) int {
	offset := 8
	if probeId == 2 {
		offset = 13
	}

	temp := make([]int, 5)

	for i := 0; i < 5; i++ {
		switch data[i+offset] {
		case 5:
			temp[i] = 0
			break
		case 6:
			temp[i] = 1
			break
		case 9:
			temp[i] = 2
			break
		case 10:
			temp[i] = 3
			break
		}
	}

	result := 0
	result += temp[0] * 256
	result += temp[1] * 64
	result += temp[2] * 16
	result += temp[3] * 4
	result += temp[4]
	result -= 532

	return result
}
