package bytesutil

import "fmt"

const (
	KILO int64 = 1000        // 1000 power 1 (10 power 3)
	KIBI int64 = 1024        // 1024 power 1 (2 power 10)
	MEGA       = KILO * KILO // 1000 power 2 (10 power 6)
	MEBI       = KIBI * KIBI // 1024 power 2 (2 power 20)
	GIGA       = MEGA * KILO // 1000 power 3 (10 power 9)
	GIBI       = MEBI * KIBI // 1024 power 3 (2 power 30)
	TERA       = GIGA * KILO // 1000 power 4 (10 power 12)
	TEBI       = GIBI * KIBI // 1024 power 4 (2 power 40)
	PETA       = TERA * KILO // 1000 power 5 (10 power 15)
	PEBI       = TEBI * KIBI // 1024 power 5 (2 power 50)
	EXA        = PETA * KILO // 1000 power 6 (10 power 18)
	EXBI       = PEBI * KIBI // 1024 power 6 (2 power 60)
)

func BinaryFormat(size int64) string {
	if size < 0 {
		return ""
	} else if size < KIBI {
		return fmt.Sprintf("%d B", size)
	} else if size < MEBI {
		return fmt.Sprintf("%.2f KiB", float64(size)/float64(KIBI))
	} else if size < GIBI {
		return fmt.Sprintf("%.2f MiB", float64(size)/float64(MEBI))
	} else if size < TEBI {
		return fmt.Sprintf("%.2f GiB", float64(size)/float64(GIBI))
	} else if size < PEBI {
		return fmt.Sprintf("%.2f TiB", float64(size)/float64(TEBI))
	} else if size < EXBI {
		return fmt.Sprintf("%.2f PiB", float64(size)/float64(PEBI))
	} else {
		return fmt.Sprintf("%.2f EiB", float64(size)/float64(EXBI))
	}
}

func DecimalFormat(size int64) string {
	if size < 0 {
		return ""
	} else if size < KILO {
		return fmt.Sprintf("%d B", size)
	} else if size < MEGA {
		return fmt.Sprintf("%.2f KB", float64(size)/float64(KILO))
	} else if size < GIGA {
		return fmt.Sprintf("%.2f MB", float64(size)/float64(MEGA))
	} else if size < TERA {
		return fmt.Sprintf("%.2f GB", float64(size)/float64(GIGA))
	} else if size < PETA {
		return fmt.Sprintf("%.2f TB", float64(size)/float64(TERA))
	} else if size < EXA {
		return fmt.Sprintf("%.2f PB", float64(size)/float64(PETA))
	} else {
		return fmt.Sprintf("%.2f EB", float64(size)/float64(EXA))
	}
}
