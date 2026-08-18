# Test-only image for the milestone-4 hermetic topology: the binary under
# test plus the topology's own forward proxy, on a minimal base. The
# production image is built with melange+apko (ADR-0011); this one exists
# only so the m4 topology can run both programs inside its segments.
#
# The directory layout mirrors the reference chart's defaults
# (deploy/charts/tobby/values.yaml: persistence.storage.mountPath and
# persistence.state.mountPath), because the m4 instance under test is
# configured by the chart's own rendered output — a container whose paths
# disagreed with the chart would be testing something else.
FROM alpine:3.22
RUN adduser -D -u 65532 tobby \
    && mkdir -p /var/lib/tobby/store /var/lib/tobby/state /etc/tobby \
    && chown -R 65532 /var/lib/tobby
COPY tobby /usr/bin/tobby
COPY tobby-proxy /usr/bin/tobby-proxy
USER 65532
ENTRYPOINT ["/usr/bin/tobby"]
