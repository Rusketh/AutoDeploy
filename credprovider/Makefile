# Cross-compile the AutoDeploy setup-lock credential provider (a Windows COM
# DLL) with MinGW-w64, so CI builds it on ubuntu alongside the Go components
# with no Windows runner. The DLL self-registers via regsvr32 (SetupComplete.cmd
# runs that at deploy time).
# Hard-assigned (not ?=) so make's built-in CXX=g++ default doesn't win; CI can
# still override on the command line (make CXX=...).
CXX      := x86_64-w64-mingw32-g++
GOOS     ?= windows
GOARCH   ?= amd64
OUT      ?= bin/autodeploy-credprovider-$(GOOS)-$(GOARCH).dll
SRC      := credprovider.cpp lockstate.cpp fullscreen.cpp
DEF      := credprovider.def
CXXFLAGS := -std=c++17 -DUNICODE -D_UNICODE -O2 -Wall
# Static libgcc/libstdc++ so the DLL has no MinGW runtime dependency on the
# target machine (a freshly imaged Windows box has none of those DLLs).
LDFLAGS  := -shared -static -static-libgcc -static-libstdc++
LIBS     := -lole32 -loleaut32 -luuid -lgdi32 -luser32 -ladvapi32

.PHONY: build clean
build: $(OUT)

$(OUT): $(SRC) $(DEF) lockstate.h fullscreen.h
	@mkdir -p $(dir $(OUT))
	$(CXX) $(CXXFLAGS) $(LDFLAGS) -o $(OUT) $(SRC) $(DEF) $(LIBS)

clean:
	rm -rf bin
