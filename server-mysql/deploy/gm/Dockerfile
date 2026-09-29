FROM scratch

COPY --chmod=0555 gm-api /gm-api
COPY --chmod=0555 gm-admin /gm-admin

USER 65532:65532
EXPOSE 8080/tcp

ENTRYPOINT ["/gm-api"]
