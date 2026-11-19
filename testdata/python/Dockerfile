# instrumenta/openapi2jsonschema plus no-properties.patch, which skips
# definitions without "properties" instead of crashing on OpenShift specs.
FROM python:3.12-alpine

ARG OPENAPI2JSONSCHEMA_REF=d697cbff8a25f520e125e3a5f79cb4e9b972e8ce

RUN apk add --no-cache git \
 && git clone https://github.com/instrumenta/openapi2jsonschema.git /src \
 && git -C /src checkout "$OPENAPI2JSONSCHEMA_REF"

COPY no-properties.patch /tmp/
RUN git -C /src apply /tmp/no-properties.patch \
 && pip install --no-cache-dir "jsonref==0.2" "pyyaml>=5.1" "click>=7,<8" "colorama>=0.4"

ENV PYTHONPATH=/src
WORKDIR /out
ENTRYPOINT ["python", "-m", "openapi2jsonschema.command"]
