using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Plaitway.AppCore.ViewModels;
using Windows.Foundation;

namespace Plaitway.App.Controls;

/// <summary>The last two minutes of traffic as a small chart; the numbers around it are what a screen reader is given.</summary>
internal sealed partial class TrafficSparkline : UserControl
{
    public static readonly DependencyProperty ChartProperty = DependencyProperty.Register(
        nameof(Chart), typeof(TrafficChart), typeof(TrafficSparkline), new PropertyMetadata(TrafficChart.Empty, (sender, _) => ((TrafficSparkline)sender).Draw()));

    public TrafficSparkline()
    {
        InitializeComponent();
    }

    /// <summary>What to draw, in the unit square.</summary>
    public TrafficChart Chart
    {
        get => (TrafficChart)GetValue(ChartProperty);
        set => SetValue(ChartProperty, value);
    }

    private void OnSizeChanged(object sender, SizeChangedEventArgs args) => Draw();

    private void Draw()
    {
        var width = ActualWidth;
        var height = ActualHeight;
        if (width <= 0 || height <= 0)
        {
            return;
        }

        var received = ToPoints(Chart.Received, width, height);
        ReceivedLine.Points = received;
        SentLine.Points = ToPoints(Chart.Sent, width, height);
        var area = new PointCollection();
        if (Chart.Received.Count > 0)
        {
            area.Add(new Point(received[0].X, height));
            foreach (var point in received)
            {
                area.Add(point);
            }

            area.Add(new Point(received[^1].X, height));
        }

        ReceivedArea.Points = area;
    }

    private static PointCollection ToPoints(IReadOnlyList<ChartPoint> points, double width, double height)
    {
        var result = new PointCollection();
        foreach (var point in points)
        {
            result.Add(new Point(point.X * width, height - (point.Y * height)));
        }

        return result;
    }
}
