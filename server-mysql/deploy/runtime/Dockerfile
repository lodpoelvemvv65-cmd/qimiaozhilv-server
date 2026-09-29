FROM scratch

COPY --chmod=0555 mhqserver /mhqserver

USER 65532:65532
EXPOSE 7756/tcp

ENTRYPOINT ["/mhqserver"]
