FROM python:3.11-alpine

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app

# Install minimal OS prerequisites and create non-root user
RUN apk add --no-cache ca-certificates tzdata && update-ca-certificates \
    && addgroup -S appgroup && adduser -S appuser -G appgroup

# Copy dependency manifest first and install Python dependencies
COPY requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r /app/requirements.txt

# Copy driver source code
COPY . /app

# Ensure correct permissions
RUN chown -R appuser:appgroup /app

USER appuser

EXPOSE 8080

CMD ["python", "driver.py"]