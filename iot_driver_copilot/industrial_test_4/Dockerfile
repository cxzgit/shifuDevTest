FROM python:3.11-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app

# Copy driver code and dependency file first
COPY requirements.txt ./
COPY . ./

# Install Python dependencies
RUN pip install --no-cache-dir -r requirements.txt

# Default HTTP port (can be overridden via env HTTP_PORT)
EXPOSE 8080

# Run the driver
CMD ["python", "driver.py"]
