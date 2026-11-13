ARG PYTHON_IMAGE=python:3.12-slim
FROM ${PYTHON_IMAGE}
WORKDIR /app
COPY *.py /app/ops/archive_v3/
COPY web/dist /app/ops/archive_v3/web/dist
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
USER 65532:65532
CMD ["python","-m","ops.archive_v3.service","--data","/archive-data","--config","/run/secrets/archive-v3.json","--bind","0.0.0.0","--port","8793"]
