FROM python:3.11-alpine

ENV PYTHONDONTWRITEBYTECODE=1
ENV PYTHONUNBUFFERED=1

WORKDIR /app

# Copy driver code and dependency manifest
COPY requirements.txt config.py driver.py README.md ./

# Install Python dependencies
RUN pip install --no-cache-dir --upgrade pip && \
    pip install --no-cache-dir -r requirements.txt

# Default exposed port (HTTP_PORT should be provided via env at runtime)
EXPOSE 8000

# Run with Gunicorn in production; binds using env HTTP_HOST and HTTP_PORT
CMD ["sh", "-c", "gunicorn --workers 1 --threads 4 --timeout 60 --bind ${HTTP_HOST}:${HTTP_PORT} driver:app"]