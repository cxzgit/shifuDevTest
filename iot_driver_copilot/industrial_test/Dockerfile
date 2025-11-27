FROM python:3.11-alpine

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app

# Copy dependency manifest first and install dependencies
COPY requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r requirements.txt \
    && addgroup -S appgroup && adduser -S appuser -G appgroup

# Copy driver source code
COPY . /app

USER appuser

EXPOSE 8080

CMD ["python", "driver.py"]