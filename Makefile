detect-and-classify: *.go */*.go go.*
	# the executable
	go build -o $@ -ldflags "-s -w" -tags osusergo,netgo

module.tar.gz: detect-and-classify
	# the bundled module
	rm -f $@
	tar czf $@ $^

clean:
	rm -f detect-and-classify

android:
	make -f Makefile_Android