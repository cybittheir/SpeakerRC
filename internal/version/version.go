package version

var (
	Name    = "Speakers Remote Control"
	Version = "0.1.0"
	Build   = "96"
)

func String() string {
	return Name + " v." + Version + " build " + Build
}
