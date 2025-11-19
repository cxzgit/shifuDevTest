CXX := g++
CXXFLAGS := -std=c++17 -O2 -pthread
SRC := driver.cpp config.cpp serial_port.cpp modbus_rtu.cpp http_server.cpp
TARGET := driver

all: $(TARGET)

$(TARGET): $(SRC)
	$(CXX) $(CXXFLAGS) $(SRC) -o $(TARGET)

clean:
	rm -f $(TARGET)
