FROM python:3.14-slim
ARG GATEWAY_UID=1000
ARG GATEWAY_GID=1000
RUN mkdir -p /data/work && chown -R ${GATEWAY_UID}:${GATEWAY_GID} /data
WORKDIR /app
COPY gateway.py /app/gateway.py
USER ${GATEWAY_UID}:${GATEWAY_GID}
ENV PYTHONDONTWRITEBYTECODE=1 TMPDIR=/data/work TMP=/data/work TEMP=/data/work
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8799/health',timeout=2)"
CMD ["python", "/app/gateway.py"]
