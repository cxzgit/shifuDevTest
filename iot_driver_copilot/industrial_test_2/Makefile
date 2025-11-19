CXX ?= g++
CXXFLAGS ?= -std=c++17 -O2 -Wall -Wextra -pthread
LDFLAGS ?= -pthread

SOURCES = driver.cpp serial_port.cpp modbus_rtu.cpp http_server.cpp config.cpp
HEADERS = config.h serial_port.h modbus_rtu.h http_server.h
OBJECTS = $(SOURCES:.cpp=.o)
TARGET = driver

all: $(TARGET)

$(TARGET): $(OBJECTS)
	$(CXX) $(OBJECTS) -o $(TARGET) $(LDFLAGS)

%.o: %.cpp $(HEADERS)
	$(CXX) $(CXXFLAGS) -c $< -o $@

clean:
	rm -f $(OBJECTS) $(TARGET)
