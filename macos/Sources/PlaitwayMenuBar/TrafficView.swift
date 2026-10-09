import Charts
import PlaitwayClient
import SwiftUI

/// How much is moving through the tunnel now, with the last two minutes behind it: the rates
/// first, the totals after them.
struct TrafficView: View {
    let profile: Profile
    @Environment(AppModel.self) private var model

    var body: some View {
        let samples = model.traffic.samples[profile.id] ?? []
        let rate = model.traffic.rate(for: profile.id)
        VStack(alignment: .leading, spacing: 10) {
            // Two columns of one width, so that the numbers of one do not push the other.
            HStack(alignment: .firstTextBaseline, spacing: 20) {
                Direction(symbol: "arrow.down", name: Text("Received", bundle: .module), rate: rate?.received, total: profile.status.rxBytes)
                    .frame(maxWidth: .infinity, alignment: .leading)
                Direction(symbol: "arrow.up", name: Text("Sent", bundle: .module), rate: rate?.sent, total: profile.status.txBytes)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            TrafficChart(samples: samples)
                .frame(height: 56)
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("profile.traffic")
    }

    private struct Direction: View {
        let symbol: String
        let name: Text
        let rate: Double?
        let total: UInt64

        var body: some View {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 4) {
                    Image(systemName: symbol).foregroundStyle(.secondary).accessibilityHidden(true)
                    name.foregroundStyle(.secondary)
                }
                .font(.caption)
                Text(verbatim: rate.map { Formatting.rate($0) } ?? "–")
                    .font(.title3.weight(.semibold))
                    .monospacedDigit()
                Text(verbatim: Formatting.bytes(total))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
            }
        }
    }
}

/// A sparkline of both directions on one scale: received as a filled area, sent as a line, so
/// that the two differ by shape and not by colour. It has no axes; the numbers are above it.
private struct TrafficChart: View {
    let samples: [TrafficHistory.Sample]

    private static let received = "received"
    private static let sent = "sent"

    var body: some View {
        let peak = max(samples.map { max($0.received, $0.sent) }.max() ?? 0, 1024)
        Chart {
            ForEach(samples) { sample in
                AreaMark(
                    x: .value("Time", sample.date),
                    yStart: .value("Floor", 0),
                    yEnd: .value("Rate", sample.received),
                    series: .value("Direction", Self.received)
                )
                .foregroundStyle(Color.accentColor.opacity(0.25))
                LineMark(
                    x: .value("Time", sample.date),
                    y: .value("Rate", sample.received),
                    series: .value("Direction", Self.received)
                )
                .foregroundStyle(Color.accentColor)
                LineMark(
                    x: .value("Time", sample.date),
                    y: .value("Rate", sample.sent),
                    series: .value("Direction", Self.sent)
                )
                .foregroundStyle(Color.secondary)
                .lineStyle(StrokeStyle(lineWidth: 1.5, dash: [4, 3]))
            }
        }
        .chartYScale(domain: 0...peak)
        .chartXAxis(.hidden)
        .chartYAxis(.hidden)
        .chartLegend(.hidden)
        .chartPlotStyle { plot in plot.background(.quaternary.opacity(0.4), in: .rect(cornerRadius: 6)) }
        .accessibilityHidden(true)
    }
}
