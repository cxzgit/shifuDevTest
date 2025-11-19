CXX := g++
CXXFLAGS := -std=c++17 -O2 -Wall -Wextra -pthread
LDFLAGS := -pthread

SRC := driver.cpp
BIN := driver

all: $(BIN)

$(BIN): $(SRC)
	$(CXX) $(CXXFLAGS) -o $(BIN) $(SRC) $(LDFLAGS)

clean:
	rm -f $(BIN)
