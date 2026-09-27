module example.com/gordis-application-plugin/host

go 1.27

require (
	example.com/gordis-application-plugin v0.0.0
	github.com/RussellLuo/gordis v0.0.0
)

replace example.com/gordis-application-plugin => ..

replace github.com/RussellLuo/gordis => ../../..
